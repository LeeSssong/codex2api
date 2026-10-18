package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
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
const basispointsMaxImageBytes = 10 << 20

// BasispointsImageHost stores inbound images for the Basispoints channel.
type BasispointsImageHost struct {
	// Host persists one decoded image and returns an absolute HTTPS URL that
	// OpenAI's servers can fetch for the lifetime of the current request.
	Host func(ctx context.Context, data []byte, mime string) (string, error)
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

func basispointsImageHostAvailable() bool { return basispointsImageHost.Load() != nil }

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
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, stats, nil
	}
	host := basispointsImageHost.Load()
	links := make(map[[sha256.Size]byte]string)
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
				if host == nil {
					return body, stats, errBasispointsImageHostUnavailable
				}
				data, mime, err := decodeInlineImage(source.String())
				if err != nil {
					return body, stats, err
				}
				sum := sha256.Sum256(data)
				link, seen := links[sum]
				if seen {
					stats.reused++
				} else {
					link, err = host.Host(ctx, data, mime)
					if err != nil {
						return body, stats, fmt.Errorf("Basispoints image host could not store an image: %w", err)
					}
					if !isPublicHTTPSLink(link) {
						return body, stats, errors.New("Basispoints image host returned a link that is not an absolute HTTPS URL")
					}
					links[sum] = link
					stats.converted++
				}
				path := fmt.Sprintf("input.%d.%s.%d", i, field, j)
				if body, err = sjson.SetBytes(body, path+".image_url", link); err != nil {
					return body, stats, fmt.Errorf("Basispoints image host could not rewrite the request: %w", err)
				}
				body, _ = sjson.DeleteBytes(body, path+".file_id")
			}
		}
	}
	return body, stats, nil
}

// decodeInlineImage decodes data:image/...;base64,... and proves the payload is a
// bounded raster image by sniffing its bytes. Errors describe shape only.
func decodeInlineImage(raw string) ([]byte, string, error) {
	raw = strings.TrimSpace(raw)
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
		return nil, "", fmt.Errorf("Basispoints embedded image exceeds the %d MB limit", basispointsMaxImageBytes>>20)
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
		return nil, "", fmt.Errorf("Basispoints embedded image must be between 1 byte and %d MB", basispointsMaxImageBytes>>20)
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", errors.New("Basispoints embedded image payload is not a recognized image format")
	}
	return data, mime, nil
}

// isPublicHTTPSLink accepts only what validateImage will accept downstream.
func isPublicHTTPSLink(link string) bool {
	parsed, err := url.Parse(link)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && strings.TrimSpace(link) == link
}
