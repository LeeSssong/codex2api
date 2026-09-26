package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/basispoints"
	"github.com/codex2api/internal/imagestore"
	"github.com/codex2api/internal/signedasset"
	"github.com/tidwall/gjson"
)

// solidPNG returns a real encoded PNG; 1x1 placeholders are rejected by some
// validators, so fixtures use a small solid square.
func solidPNG(t *testing.T, size int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dataURL(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

type fakeImageHost struct {
	calls [][]byte
	mimes []string
	err   error
}

func (f *fakeImageHost) install(t *testing.T) {
	t.Helper()
	previous := basispointsImageHost.Load()
	SetBasispointsImageHost(&BasispointsImageHost{Host: func(_ context.Context, data []byte, mime string) (string, error) {
		if f.err != nil {
			return "", f.err
		}
		f.calls = append(f.calls, append([]byte(nil), data...))
		f.mimes = append(f.mimes, mime)
		return fmt.Sprintf("https://img.example/p/img/%d?exp=1&sig=abc", len(f.calls)), nil
	}})
	t.Cleanup(func() { basispointsImageHost.Store(previous) })
}

func captureProxyLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &buf
}

func TestBasispointsRewritesEmbeddedImagesInMessagesAndToolResults(t *testing.T) {
	host := &fakeImageHost{}
	host.install(t)
	red, blue := solidPNG(t, 8, color.RGBA{R: 255, A: 255}), solidPNG(t, 8, color.RGBA{B: 255, A: 255})
	body := []byte(`{"model":"gpt-6-astra","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"` + dataURL(red) + `","detail":"high","file_id":"file_1"}]},
		{"type":"function_call","call_id":"c1","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":[{"type":"input_image","image_url":"` + dataURL(red) + `"},{"type":"input_image","image_url":"` + dataURL(blue) + `","detail":"low"}]},
		{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://images.example/kept.png","detail":"auto"}]},
		{"type":"function_call","call_id":"c2","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"c2","output":"plain text result"}]}`)
	out, stats, err := rewriteBasispointsImages(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	if stats.converted != 2 || stats.reused != 1 || len(host.calls) != 2 {
		t.Fatalf("converted=%d reused=%d calls=%d", stats.converted, stats.reused, len(host.calls))
	}
	if !bytes.Equal(host.calls[0], red) || !bytes.Equal(host.calls[1], blue) || host.mimes[0] != "image/png" {
		t.Fatal("host did not receive the decoded image bytes with their sniffed type")
	}
	if strings.Contains(string(out), "data:image") || strings.Contains(string(out), base64.StdEncoding.EncodeToString(red)[:32]) {
		t.Fatal("embedded image data survived the rewrite")
	}
	first := gjson.GetBytes(out, "input.0.content.1")
	if first.Get("image_url").String() != "https://img.example/p/img/1?exp=1&sig=abc" || first.Get("detail").String() != "high" || first.Get("file_id").Exists() {
		t.Fatalf("user image not rewritten correctly: %s", first.Raw)
	}
	if gjson.GetBytes(out, "input.2.output.0.image_url").String() != "https://img.example/p/img/1?exp=1&sig=abc" || gjson.GetBytes(out, "input.2.output.1.image_url").String() != "https://img.example/p/img/2?exp=1&sig=abc" || gjson.GetBytes(out, "input.2.output.1.detail").String() != "low" {
		t.Fatalf("tool result images not rewritten or deduplicated: %s", gjson.GetBytes(out, "input.2.output").Raw)
	}
	if gjson.GetBytes(out, "input.3.content.0.image_url").String() != "https://images.example/kept.png" || gjson.GetBytes(out, "input.5.output").String() != "plain text result" || gjson.GetBytes(out, "input.0.content.0.text").String() != "look" {
		t.Fatal("HTTPS images, text results or neighboring text were changed")
	}
	if _, _, err := basispoints.Prepare(out, "scope", nil); err != nil {
		t.Fatalf("rewritten body must pass the bridge unchanged: %v", err)
	}
	same, stats, err := rewriteBasispointsImages(context.Background(), []byte(`{"model":"gpt-6-astra","input":"hello"}`))
	if err != nil || stats.converted != 0 || string(same) != `{"model":"gpt-6-astra","input":"hello"}` {
		t.Fatal("bodies without embedded images must pass through untouched")
	}
}

func TestBasispointsEmbeddedImageDecodingRejectsNonImagesWithoutLeaking(t *testing.T) {
	const private = "PRIVATE_IMAGE_BYTES_SENTINEL"
	encodedText := base64.StdEncoding.EncodeToString([]byte(private + " is not an image"))
	for name, raw := range map[string]string{
		"text payload":      "data:image/png;base64," + encodedText,
		"non image type":    "data:text/plain;base64," + base64.StdEncoding.EncodeToString(solidPNG(t, 4, color.RGBA{A: 255})),
		"not base64":        "data:image/png;base64," + private + "!!!",
		"missing payload":   "data:image/png;base64",
		"not base64 marked": "data:image/png," + private,
		"empty":             "data:image/png;base64,",
		"too large":         "data:image/png;base64," + strings.Repeat("A", basispointsMaxImageBytes/3*4+8),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := decodeInlineImage(raw)
			if err == nil {
				t.Fatal("invalid embedded image accepted")
			}
			if strings.Contains(err.Error(), private) || strings.Contains(err.Error(), encodedText[:16]) {
				t.Fatalf("image payload leaked into the error: %v", err)
			}
			if basispoints.Category(err) != "image_input" {
				t.Fatalf("category = %s", basispoints.Category(err))
			}
		})
	}
	wrapped := "data:image/png;base64," + strings.Join(splitEvery(base64.StdEncoding.EncodeToString(solidPNG(t, 4, color.RGBA{G: 255, A: 255})), 16), "\n")
	if data, mime, err := decodeInlineImage(wrapped); err != nil || mime != "image/png" || len(data) == 0 {
		t.Fatalf("line-wrapped base64 must decode: %v", err)
	}
	if data, mime, err := decodeInlineImage("data:;base64," + base64.RawURLEncoding.EncodeToString(solidPNG(t, 4, color.RGBA{A: 255}))); err != nil || mime != "image/png" || len(data) == 0 {
		t.Fatalf("type-less URL-safe base64 must decode by sniffing: %v", err)
	}
}

func splitEvery(s string, n int) []string {
	var parts []string
	for len(s) > n {
		parts, s = append(parts, s[:n]), s[n:]
	}
	return append(parts, s)
}

// With an image host the request stays on Basispoints: the wire body carries the
// hosted HTTPS link, the data URL is gone, and nothing about the image reaches logs.
func TestBasispointsSendsHostedImagesUpstreamWithoutFallback(t *testing.T) {
	enableBasispointsForTest(t)
	host := &fakeImageHost{}
	host.install(t)
	logs := captureProxyLogs(t)
	account := &auth.Account{DBID: 91260, AccountID: "workspace", AccessToken: "test-token"}
	payload := base64.StdEncoding.EncodeToString(solidPNG(t, 8, color.RGBA{R: 200, G: 10, B: 10, A: 255}))
	var wire []byte
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		wire, _ = io.ReadAll(req.Body)
		return basispointsTestCompleted("resp_img", nil), nil
	})
	installClaudeBoundaryTransport(t, account, func(*http.Request) (*http.Response, error) {
		t.Fatal("a convertible image must not fall back to the Codex channel")
		return nil, nil
	})
	body := `{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe"},{"type":"input_image","image_url":"data:image/png;base64,` + payload + `","detail":"auto"}]}]}`
	resp, err := ExecuteRequest(context.Background(), account, []byte(body), "", "", "client-key", nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.Header.Get("X-Codex2API-Upstream") != "basispoints" || resp.Header.Get(basispointsBypassHeader) != "" {
		t.Fatalf("hosted image request left Basispoints: %v", resp.Header)
	}
	// The user message is the last wire input item after the developer prologue.
	sent := gjson.GetBytes(wire, "input.@reverse.0.content.1")
	if len(host.calls) != 1 || sent.Get("image_url").String() != "https://img.example/p/img/1?exp=1&sig=abc" || sent.Get("detail").String() != "auto" || strings.Contains(string(wire), "data:image") || strings.Contains(string(wire), payload[:32]) {
		t.Fatalf("wire body did not carry the hosted link: calls=%d item=%s", len(host.calls), sent.Raw)
	}
	if !strings.Contains(logs.String(), "stage=images result=hosted converted=1") {
		t.Fatalf("hosting was not logged: %s", logs.String())
	}
	if strings.Contains(logs.String(), payload[:32]) || strings.Contains(logs.String(), "data:image") || strings.Contains(logs.String(), "img.example") {
		t.Fatalf("image data or links leaked into logs: %s", logs.String())
	}
}

func TestBasispointsImageHostFailureReturnsCapacityError(t *testing.T) {
	host := &fakeImageHost{err: errors.New("disk full private filesystem detail")}
	host.install(t)
	body := []byte(fmt.Sprintf("{\"input\":[{\"type\":\"message\",\"content\":[{\"type\":\"input_image\",\"image_url\":%q}]}]}", dataURL(solidPNG(t, 4, color.RGBA{A: 255}))))
	_, _, err := rewriteBasispointsImages(context.Background(), body)
	status, _, ok := basispointsImageErrorStatus(err)
	if !ok || status != 503 || strings.Contains(err.Error(), "private filesystem") {
		t.Fatalf("storage error must be sanitized local 503: %v", err)
	}
	SetBasispointsImageHost(nil)
	_, _, err = rewriteBasispointsImages(context.Background(), body)
	if !errors.Is(err, errBasispointsImageHostUnavailable) {
		t.Fatalf("missing host classification: %v", err)
	}
}

func TestBasispointsImageHostRequiresPublicHTTPSBase(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, base := range []string{"", "http://plain.example", "not a url"} {
		t.Setenv("IMAGE_ASSET_PUBLIC_BASE_URL", base)
		if host := NewBasispointsImageHost(db); host == nil || host.Available(context.Background()) {
			t.Fatalf("base %q must not enable the image host", base)
		}
	}
	if NewBasispointsImageHost(nil) != nil {
		t.Fatal("a host needs a database for asset rows")
	}
}

func TestBasispointsBuiltInHostStoresDeduplicatesAndSweeps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("IMAGE_ASSET_DIR", dir)
	t.Setenv("IMAGE_ASSET_PUBLIC_BASE_URL", "https://gallery.example/")
	t.Setenv("IMAGE_ASSET_SIGNING_SECRET", "persistent-test-secret")
	if err := imagestore.Configure(imagestore.Config{Backend: imagestore.BackendLocal, LocalDir: dir}); err != nil {
		t.Fatal(err)
	}
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := WithBasispointsImageScope(context.Background(), "tenant-one")
	if err := db.SaveBasispointsSettings(ctx, database.BasispointsSettings{Enabled: true, ModelScope: "all", ImageRelayEnabled: true, ImageRelayPublicOrigin: "https://cdn.example"}); err != nil {
		t.Fatal(err)
	}
	host := NewBasispointsImageHost(db)
	if host == nil {
		t.Fatal("HTTPS base must enable the host")
	}
	store := &basispointsImageStore{db: db}
	red := solidPNG(t, 6, color.RGBA{R: 255, A: 255})
	first, err := store.host(ctx, red, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(first)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "cdn.example" || !strings.HasPrefix(parsed.Path, "/p/img/") {
		t.Fatalf("hosted link is not a public signed asset link: %s", first)
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(parsed.Path, "/p/img/"), 10, 64)
	exp, _ := strconv.ParseInt(parsed.Query().Get("exp"), 10, 64)
	if !signedasset.VerifyImageAssetURL(id, exp, 0, parsed.Query().Get("sig"), time.Now()) {
		t.Fatal("hosted link signature does not verify")
	}
	if ttl := time.Until(time.Unix(exp, 0)); ttl > basispointsImageURLTTL+time.Minute || ttl < basispointsImageURLTTL-time.Minute {
		t.Fatalf("link TTL = %s, want about %s", ttl, basispointsImageURLTTL)
	}
	asset, err := db.GetImageAsset(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Model != basispointsImageModel || asset.MimeType != "image/png" || asset.Bytes != len(red) || asset.Width != 6 || asset.Height != 6 || asset.JobID != 0 {
		t.Fatalf("asset row not tagged as inbound: %+v", asset)
	}
	if stored, err := os.ReadFile(asset.StoragePath); err != nil || !bytes.Equal(stored, red) || filepath.Dir(asset.StoragePath) != dir {
		t.Fatalf("image not stored under the asset directory: %v", err)
	}
	// Identical bytes within the reuse window share the asset and get a fresh link.
	second, err := store.host(ctx, red, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, fmt.Sprintf("/p/img/%d?", id)) {
		t.Fatalf("identical image was stored again: %s", second)
	}
	// Distinct data and distinct tenant scopes each get their own asset.
	if third, err := store.host(ctx, solidPNG(t, 6, color.RGBA{B: 255, A: 255}), "image/png"); err != nil || strings.Contains(third, fmt.Sprintf("/p/img/%d?", id)) {
		t.Fatalf("distinct data reused: %v", err)
	}
	restarted := &basispointsImageStore{db: db}
	if again, err := restarted.host(ctx, red, "image/png"); err != nil || !strings.Contains(again, fmt.Sprintf("/p/img/%d?", id)) {
		t.Fatalf("restart did not reuse persisted asset: %v", err)
	}
	if other, err := restarted.host(WithBasispointsImageScope(context.Background(), "tenant-two"), red, "image/png"); err != nil || strings.Contains(other, fmt.Sprintf("/p/img/%d?", id)) {
		t.Fatalf("cross-scope reuse: %v", err)
	}
	page, err := db.ListImageAssets(context.Background(), 1, 50, 0)
	if err != nil || page.Total != 0 {
		t.Fatalf("expected no inbound assets in gallery, got %d (%v)", page.Total, err)
	}
	// Sweeping with a future cutoff removes rows and files; a second pass is a no-op.
	removed, err := sweepBasispointsImages(context.Background(), db, time.Now().Add(time.Hour))
	if err != nil || removed != 3 {
		t.Fatalf("sweep removed %d, %v", removed, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("stored files survived the sweep: %d", len(entries))
	}
	if removed, err := sweepBasispointsImages(context.Background(), db, time.Now().Add(time.Hour)); err != nil || removed != 0 {
		t.Fatalf("second sweep removed %d, %v", removed, err)
	}
	if _, err := db.GetImageAsset(context.Background(), id); err == nil {
		t.Fatal("swept asset row still exists")
	}
}
