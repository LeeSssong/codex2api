package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

type basispointsEmbeddedImage struct {
	path, raw string
	sum       [sha256.Size]byte
}
type basispointsImageError struct {
	status  int
	message string
}

func (e *basispointsImageError) Error() string { return e.message }
func imageInputError(status int, message string) error {
	return &basispointsImageError{status: status, message: message}
}
func basispointsImageErrorStatus(err error) (int, string, bool) {
	var e *basispointsImageError
	if errors.As(err, &e) {
		code := "invalid_image"
		if e.status == 503 {
			code = "image_capacity_exhausted"
		}
		if e.status == 413 {
			code = "image_too_large"
		}
		return e.status, code, true
	}
	if err != nil && !errors.Is(err, errBasispointsImageHostUnavailable) {
		return 400, "invalid_image", true
	}
	return 0, "", false
}

type basispointsImageScopeKey struct{}

// WithBasispointsImageScope isolates content reuse by caller/tenant. Only the
// SHA-256 scope is retained in metadata; no raw API key or identity is persisted.
func WithBasispointsImageScope(ctx context.Context, scope string) context.Context {
	sum := sha256.Sum256([]byte(scope))
	return context.WithValue(ctx, basispointsImageScopeKey{}, hex.EncodeToString(sum[:]))
}
func basispointsImageScope(ctx context.Context) string {
	s, _ := ctx.Value(basispointsImageScopeKey{}).(string)
	return s
}
