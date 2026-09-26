package signedasset

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	imageAssetPathPrefix       = "/p/img"
	imageAssetPublicBaseURLEnv = "IMAGE_ASSET_PUBLIC_BASE_URL"
	imageAssetSigningSecretEnv = "IMAGE_ASSET_SIGNING_SECRET"
	defaultImageAssetTTL       = 24 * time.Hour
)

var (
	secretOnce sync.Once
	secret     []byte
)

func ImageAssetURL(assetID int64, thumbKB int) string {
	return ImageAssetURLWithTTL(assetID, thumbKB, defaultImageAssetTTL)
}

func ImageAssetURLWithTTL(assetID int64, thumbKB int, ttl time.Duration) string {
	if assetID <= 0 {
		return ""
	}
	if ttl <= 0 {
		ttl = defaultImageAssetTTL
	}
	exp := time.Now().Add(ttl).Unix()
	sig := imageAssetSignature(assetID, exp, thumbKB)
	if thumbKB > 0 {
		return publicImageAssetURL(fmt.Sprintf("%s/%d?exp=%d&thumb_kb=%d&sig=%s", imageAssetPathPrefix, assetID, exp, thumbKB, sig))
	}
	return publicImageAssetURL(fmt.Sprintf("%s/%d?exp=%d&sig=%s", imageAssetPathPrefix, assetID, exp, sig))
}

// PublicBaseURL returns the validated IMAGE_ASSET_PUBLIC_BASE_URL without a
// trailing slash, or "" when it is unset or malformed. Signed links stay
// relative in that case, which only works for same-origin browsers.
func PublicBaseURL() string {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv(imageAssetPublicBaseURLEnv)), "/")
	parsed, err := url.Parse(baseURL)
	if baseURL == "" || err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return ""
	}
	return baseURL
}

func publicImageAssetURL(path string) string {
	if baseURL := PublicBaseURL(); baseURL != "" {
		return baseURL + path
	}
	return path
}

func VerifyImageAssetURL(assetID int64, exp int64, thumbKB int, sig string, now time.Time) bool {
	if assetID <= 0 || exp <= 0 || strings.TrimSpace(sig) == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	if now.Unix() > exp {
		return false
	}
	want := imageAssetSignature(assetID, exp, thumbKB)
	return hmac.Equal([]byte(want), []byte(sig))
}

func imageAssetSignature(assetID int64, exp int64, thumbKB int) string {
	mac := hmac.New(sha256.New, imageAssetSecret())
	mac.Write([]byte(strconv.FormatInt(assetID, 10)))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.FormatInt(exp, 10)))
	mac.Write([]byte("|"))
	mac.Write([]byte(strconv.Itoa(thumbKB)))
	sum := mac.Sum(nil)
	return hex.EncodeToString(sum[:16])
}

func imageAssetSecret() []byte {
	// A protected shared key takes precedence on every call, including a process
	// which previously served gallery assets before relay was configured.
	if configured := strings.TrimSpace(os.Getenv(imageAssetSigningSecretEnv)); configured != "" {
		digest := sha256.Sum256([]byte(configured))
		return digest[:]
	}
	secretOnce.Do(func() {
		if configured := strings.TrimSpace(os.Getenv(imageAssetSigningSecretEnv)); configured != "" {
			digest := sha256.Sum256([]byte(configured))
			secret = digest[:]
			return
		}
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			panic(fmt.Sprintf("signedasset: generate image proxy secret: %v", err))
		}
		// Every process gets its own key, so links break on restart and each
		// replica rejects the others' links. That surfaces to users as
		// intermittent 403s, which is impossible to diagnose without this line.
		log.Printf("signedasset: %s is not set; signed image links use a per-process random key and will not survive a restart or work across replicas", imageAssetSigningSecretEnv)
	})
	return secret
}

// HTTPSOrigin is deliberately stricter than the gallery's historical base URL.
// A relay origin cannot inject credentials, a path, query, or fragment.
func HTTPSOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("image relay origin must be an HTTPS origin without credentials, path, query or fragment")
	}
	return "https://" + u.Host, nil
}

// PersistentSigningConfigured prevents replicas from publishing links signed
// with the gallery's per-process fallback secret.
func PersistentSigningConfigured() bool {
	return strings.TrimSpace(os.Getenv(imageAssetSigningSecretEnv)) != ""
}
func ImageAssetURLAtOrigin(assetID int64, origin string, expires time.Time) string {
	origin, err := HTTPSOrigin(origin)
	if err != nil || assetID <= 0 || !PersistentSigningConfigured() {
		return ""
	}
	exp := expires.Unix()
	sig := imageAssetSignature(assetID, exp, 0)
	return fmt.Sprintf("%s%s/%d?exp=%d&sig=%s", origin, imageAssetPathPrefix, assetID, exp, sig)
}
