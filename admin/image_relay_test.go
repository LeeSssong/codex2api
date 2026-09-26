package admin

import (
	"context"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/imagestore"
	"github.com/codex2api/internal/signedasset"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSignedImageRelayRevocationAndNoStore(t *testing.T) {
	db := newTestAdminDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("IMAGE_ASSET_DIR", dir)
	t.Setenv("IMAGE_ASSET_SIGNING_SECRET", "persistent-test-secret")
	if err := imagestore.Configure(imagestore.Config{Backend: "local", LocalDir: dir}); err != nil {
		t.Fatal(err)
	}
	settings := database.BasispointsSettings{Enabled: true, ModelScope: "all", ImageRelayEnabled: true, ImageRelayPublicOrigin: "https://relay.example"}
	if err := db.SaveBasispointsSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	settings, _ = db.GetBasispointsSettings(ctx)
	file := filepath.Join(dir, "relay.png")
	if err := os.WriteFile(file, []byte("image payload"), 0600); err != nil {
		t.Fatal(err)
	}
	token, err := db.ReserveImageRelay(ctx, 13, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := db.PrepareImageRelay(ctx, token, "scope", "digest", settings.ImageRelayEpoch, database.ImageAssetInput{Filename: "relay.png", StoragePath: file, MimeType: "image/png", Bytes: 13})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.FinishImageRelay(ctx, token, true, nil, settings.ImageRelayEpoch); err != nil {
		t.Fatal(err)
	}
	raw := signedasset.ImageAssetURLAtOrigin(id, "https://relay.example", time.Now().Add(20*time.Minute))
	u, _ := url.Parse(raw)
	h := &Handler{db: db}
	r := gin.New()
	r.GET("/p/img/:id", h.GetSignedImageAssetFile)
	get := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, u.RequestURI(), nil))
		return w
	}
	w := get()
	if w.Code != 200 || w.Body.String() != "image payload" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("unsafe image response: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	thumb, _ := url.Parse(signedasset.ImageAssetURLWithTTL(id, 32, 10*time.Minute))
	tw := httptest.NewRecorder()
	r.ServeHTTP(tw, httptest.NewRequest(http.MethodGet, thumb.RequestURI(), nil))
	if tw.Code != 403 {
		t.Fatalf("relay thumbnail accepted: %d", tw.Code)
	}
	tooLong, _ := url.Parse(signedasset.ImageAssetURLAtOrigin(id, "https://relay.example", time.Now().Add(time.Hour)))
	lw := httptest.NewRecorder()
	r.ServeHTTP(lw, httptest.NewRequest(http.MethodGet, tooLong.RequestURI(), nil))
	if lw.Code != 403 {
		t.Fatalf("signature outlives metadata: %d", lw.Code)
	}
	settings.ImageRelayEnabled = false
	if err := db.SaveBasispointsSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if w := get(); w.Code != 403 {
		t.Fatalf("disabled image served: %d", w.Code)
	}
	settings.ImageRelayEnabled = true
	if err := db.SaveBasispointsSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if w := get(); w.Code != 403 {
		t.Fatalf("old image resurrected after enable: %d", w.Code)
	}
}
