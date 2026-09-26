package api

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSignedImageRecoveryDoesNotLogCredentials(t *testing.T) {
	oldMode, oldWriter, oldLog := gin.Mode(), gin.DefaultErrorWriter, log.Writer()
	t.Cleanup(func() { gin.SetMode(oldMode); gin.DefaultErrorWriter = oldWriter; log.SetOutput(oldLog) })
	const signedURL = "https://relay.example/p/img/42?exp=1790400000&sig=private-image-signature"
	for _, mode := range []string{gin.ReleaseMode, gin.DebugMode} {
		for _, tc := range []struct {
			name   string
			value  any
			broken bool
		}{
			{"epipe", fmt.Errorf("%s: %w", signedURL, syscall.EPIPE), true},
			{"reset", fmt.Errorf("%s: %w", signedURL, syscall.ECONNRESET), true},
			{"abort", fmt.Errorf("%s: %w", signedURL, http.ErrAbortHandler), true},
			{"url-string", signedURL, false},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				gin.SetMode(mode)
				var logs bytes.Buffer
				gin.DefaultErrorWriter = &logs
				log.SetOutput(&logs)
				router := gin.New()
				router.Use(RecoveryMiddleware())
				reached := false
				router.GET("/p/img/:id", func(c *gin.Context) { panic(tc.value) }, func(c *gin.Context) { reached = true })
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, signedURL, nil)
				request.Header.Set("Referer", signedURL)
				router.ServeHTTP(response, request)
				if reached {
					t.Fatal("panic continued into the next handler")
				}
				for _, secret := range []string{signedURL, "exp=", "sig=", "1790400000", "private-image-signature"} {
					if strings.Contains(logs.String(), secret) {
						t.Fatalf("recovery log leaked %q: %s", secret, logs.String())
					}
					if strings.Contains(response.Body.String(), secret) {
						t.Fatalf("response leaked %q", secret)
					}
				}
				if !tc.broken && response.Code != http.StatusInternalServerError {
					t.Fatalf("status = %d, want 500", response.Code)
				}
			})
		}
	}
}

func TestRecoveryPreservesNonImageBehavior(t *testing.T) {
	oldMode, oldWriter, oldLog := gin.Mode(), gin.DefaultErrorWriter, log.Writer()
	t.Cleanup(func() { gin.SetMode(oldMode); gin.DefaultErrorWriter = oldWriter; log.SetOutput(oldLog) })
	gin.SetMode(gin.ReleaseMode)
	var logs bytes.Buffer
	gin.DefaultErrorWriter = &logs
	log.SetOutput(&logs)
	router := gin.New()
	router.Use(RecoveryMiddleware())
	router.GET("/v1/test", func(c *gin.Context) { panic("ordinary-panic-marker") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/test", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
	if !strings.Contains(logs.String(), "Panic recovered: ordinary-panic-marker") {
		t.Fatal("existing non-image logging changed")
	}
	if strings.Contains(response.Body.String(), "ordinary-panic-marker") {
		t.Fatal("panic leaked into response")
	}
}
