package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/codex2api/security"
	_ "golang.org/x/image/webp"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/codex2api/internal/basispoints"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Basispoints rejects embedded images (data URLs, file IDs) and has no upload
// endpoint, but it fetches absolute HTTPS image URLs. The proxy therefore hosts
// inbound images itself behind the existing signed /p/img endpoint instead of
// handing them to a third party. Every hosted image is still reachable by anyone
// holding its signed link until that link expires, so links are short-lived and
// inbound assets are swept separately. Image bytes, base64 payloads and data URLs
// must never reach logs or error messages.

// basispointsMaxImageBytes bounds one decoded inbound image.
const basispointsMaxImageBytes = 20 << 20

// BasispointsImageHost stores inbound images for the Basispoints channel.
type BasispointsImageHost struct {
	// Host persists one decoded image and returns an absolute HTTPS URL that
	// OpenAI's servers can fetch for the lifetime of the current request.
	Host      func(ctx context.Context, data []byte, mime string) (string, error)
	Begin     func(context.Context, int64, int) (context.Context, func(bool) error, error)
	Available func(context.Context) bool
}

var basispointsImageHost atomic.Pointer[BasispointsImageHost]

// SetBasispointsImageHost installs the inbound image host; nil removes it, after
// which embedded images route to the original Codex channel again.
func SetBasispointsImageHost(host *BasispointsImageHost) {
	if host == nil || host.Host == nil {
		basispointsImageHost.Store(nil)
		return
	}
	basispointsImageHost.Store(host)
}

func basispointsImageHostAvailable() bool {
	h := basispointsImageHost.Load()
	return h != nil && (h.Available == nil || h.Available(context.Background()))
}

var errBasispointsImageHostUnavailable = errors.New("Basispoints image host is not configured; embedded images cannot become HTTPS links")

type basispointsImageRewrite struct {
	converted, reused int
}

// rewriteBasispointsImages replaces every data-URL input_image in message content
// and tool results with a hosted HTTPS link, so the body reaches Prepare with only
// HTTPS images. Identical bytes within one request share one link. The body is
// returned unchanged when nothing is embedded.
func rewriteBasispointsImages(ctx context.Context, body []byte) ([]byte, basispointsImageRewrite, error) {
	var stats basispointsImageRewrite
	// Reserve transient JSON working copies before parsing can allocate strings.
	workingMemory, ok := security.TryAcquireRequestMemory(int64(len(body)) * 2)
	if !ok {
		return body, stats, imageInputError(503, "Basispoints request memory capacity exhausted")
	}
	defer workingMemory.Release()
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, stats, nil
	}
	var images []basispointsEmbeddedImage
	var encoded, decodedEstimate int64
	for i, item := range input.Array() {
		for _, field := range []string{"content", "output"} {
			parts := item.Get(field)
			if !parts.IsArray() {
				continue
			}
			for j, part := range parts.Array() {
				source := part.Get("image_url")
				if part.Get("type").String() != "input_image" || source.Type != gjson.String || !basispoints.IsDataURL(source.String()) {
					continue
				}
				if len(images) >= 20 {
					return body, stats, imageInputError(413, "Basispoints embedded image request exceeds 20 images")
				}
				raw := source.String()
				if _, payload, found := strings.Cut(raw, ","); found {
					n, padding := 0, 0
					for _, ch := range payload {
						if ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t' {
							continue
						}
						n++
						if ch == '=' {
							padding++
						}
					}
					size := int64(n)*3/4 - int64(padding)
					if size > basispointsMaxImageBytes {
						return body, stats, imageInputError(413, "Basispoints embedded image exceeds 20 MiB")
					}
					decodedEstimate += size
					if decodedEstimate > 32<<20 {
						return body, stats, imageInputError(413, "Basispoints embedded image request exceeds 32 MiB")
					}
				}
				encoded += int64(len(raw))
				images = append(images, basispointsEmbeddedImage{path: fmt.Sprintf("input.%d.%s.%d", i, field, j), raw: raw})
			}
		}
	}
	if len(images) == 0 {
		return body, stats, nil
	}
	host := basispointsImageHost.Load()
	if host == nil || (host.Available != nil && !host.Available(ctx)) {
		return body, stats, errBasispointsImageHostUnavailable
	}
	memory, ok := security.TryAcquireImageRelayMemory(min(encoded*3/4, int64(basispointsMaxImageBytes)) * 2)
	if !ok {
		return body, stats, imageInputError(503, "Basispoints image request memory capacity exhausted")
	}
	defer memory.Release()

	var total, uniqueBytes int64
	seen := make(map[[sha256.Size]byte]bool)
	for i := range images {
		if err := ctx.Err(); err != nil {
			return body, stats, err
		}
		data, _, err := decodeInlineImage(images[i].raw)
		if err != nil {
			return body, stats, err
		}
		total += int64(len(data))
		if total > 32<<20 {
			return body, stats, imageInputError(413, "Basispoints embedded image request exceeds 32 MiB")
		}
		images[i].sum = sha256.Sum256(data)
		if !seen[images[i].sum] {
			uniqueBytes += int64(len(data))
			seen[images[i].sum] = true
		}
	}
	var finish func(bool) error
	if host.Begin != nil {
		var err error
		ctx, finish, err = host.Begin(ctx, uniqueBytes, len(seen))
		if err != nil {
			return body, stats, err
		}
	}
	success := false
	if finish != nil {
		defer func() {
			if !success {
				_ = finish(false)
			}
		}()
	}
	links := make(map[[sha256.Size]byte]string)
	original := body
	for _, img := range images {
		if err := ctx.Err(); err != nil {
			return original, stats, err
		}
		link, reused := links[img.sum]
		if reused {
			stats.reused++
		} else {
			data, mime, err := decodeInlineImage(img.raw)
			if err != nil {
				return original, stats, err
			}
			link, err = host.Host(ctx, data, mime)
			if err != nil {
				return original, stats, imageInputError(503, "Basispoints image storage unavailable")
			}
			if !isPublicHTTPSLink(link) {
				return original, stats, imageInputError(503, "Basispoints image host returned an invalid link")
			}
			links[img.sum] = link
			stats.converted++
		}
		var err error
		body, err = sjson.SetBytes(body, img.path+".image_url", link)
		if err != nil {
			return original, stats, imageInputError(400, "Basispoints image request rewrite failed")
		}
		body, _ = sjson.DeleteBytes(body, img.path+".file_id")
	}
	if finish != nil {
		if err := finish(true); err != nil {
			return original, stats, err
		}
	}
	success = true
	return body, stats, nil
}

// decodeInlineImage decodes data:image/...;base64,... and proves the payload is a
// bounded raster image by sniffing its bytes. Errors describe shape only.
func decodeInlineImage(raw string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 5 || !strings.EqualFold(raw[:5], "data:") {
		return nil, "", imageInputError(400, "Basispoints embedded image is not a data URL")
	}
	header, payload, found := strings.Cut(raw[len("data:"):], ",")
	if !found {
		return nil, "", errors.New("Basispoints embedded image data URL is missing its payload")
	}
	declared, params, _ := strings.Cut(header, ";")
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared != "" && !strings.HasPrefix(declared, "image/") {
		return nil, "", errors.New("Basispoints embedded image data URL does not declare an image media type")
	}
	if !strings.Contains(";"+strings.ToLower(params)+";", ";base64;") {
		return nil, "", errors.New("Basispoints embedded image data URL must be base64 encoded")
	}
	payload = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, payload)
	if len(payload) > basispointsMaxImageBytes/3*4+4 {
		return nil, "", imageInputError(413, "Basispoints embedded image exceeds 20 MiB")
	}
	var data []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if data, err = encoding.DecodeString(payload); err == nil {
			break
		}
	}
	if err != nil {
		return nil, "", errors.New("Basispoints embedded image payload is not valid base64")
	}
	if len(data) == 0 || len(data) > basispointsMaxImageBytes {
		return nil, "", imageInputError(413, "Basispoints embedded image must be between 1 byte and 20 MiB")
	}
	mime := http.DetectContentType(data)
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" && mime != "image/webp" {
		return nil, "", errors.New("Basispoints embedded image payload is not a recognized image format")
	}
	if declared != "" && declared != mime {
		return nil, "", imageInputError(400, "Basispoints embedded image MIME does not match its bytes")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || format == "" {
		return nil, "", imageInputError(400, "Basispoints embedded image has an invalid raster header")
	}
	if int64(cfg.Width)*int64(cfg.Height) > 64_000_000 {
		return nil, "", imageInputError(400, "Basispoints embedded image exceeds 64 million pixels")
	}
	return data, mime, nil
}

// isPublicHTTPSLink accepts only what validateImage will accept downstream.
func isPublicHTTPSLink(link string) bool {
	parsed, err := url.Parse(link)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && strings.TrimSpace(link) == link
}
