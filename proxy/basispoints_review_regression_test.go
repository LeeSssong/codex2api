package proxy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/codex2api/auth"
	"github.com/codex2api/config"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBasispointsMessagesImageErrorStatus(t *testing.T) {
	for _, status := range []int{400, 413, 503} {
		for _, stream := range []bool{false, true} {
			t.Run(http.StatusText(status)+map[bool]string{false: "/json", true: "/stream"}[stream], func(t *testing.T) {
				enableBasispointsForTest(t)
				gin.SetMode(gin.TestMode)
				store := auth.NewStore(nil, nil, &database.SystemSettings{MaxConcurrency: 2, CodexBasispointsEnabled: true})
				t.Cleanup(store.Stop)
				a := &auth.Account{DBID: 91289, AccountID: "fixture", AccessToken: "fixture", PlanType: "pro"}
				store.AddAccount(a)
				seedContinuousRetryLocalHealth(a)
				host := &fakeImageHost{err: errors.New("private storage")}
				host.install(t)
				h := NewHandler(store, nil, &config.Config{AllowAnonymousV1: true}, nil)
				router := gin.New()
				h.RegisterRoutes(router)
				upstream := 0
				installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
					upstream++
					return basispointsTestCompleted("unexpected", nil), nil
				})
				content := []any{}
				n := 1
				if status == 413 {
					n = 21
				}
				data := base64.StdEncoding.EncodeToString(solidPNG(t, 4, color.RGBA{A: 255}))
				if status == 400 {
					data = "invalid-base64"
				}
				for i := 0; i < n; i++ {
					content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}})
				}
				raw, _ := json.Marshal(map[string]any{"model": "gpt-6-astra", "max_tokens": 8, "stream": stream, "messages": []any{map[string]any{"role": "user", "content": content}}})
				req := httptest.NewRequest("POST", "/v1/messages", bytes.NewReader(raw))
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != status || strings.Contains(w.Body.String(), "private storage") || upstream != 0 {
					t.Fatalf("local rejection lost: status=%d upstream=%d body=%s", w.Code, upstream, w.Body)
				}
				assertContinuousRetryLocalHealthUnchanged(t, a)
			})
		}
	}
}
func TestBasispointsProbeRechecksModelBetweenSteps(t *testing.T) {
	enableBasispointsForTest(t)
	db, a := newBasispointsPolicyRouteAccount(t)
	calls := 0
	installBasispointsTransport(t, a, func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			if err := db.SetBasispointsAccountPolicy(context.Background(), a.ID(), database.BasispointsAccountPolicy{ModelScope: "selected", Models: []string{}}); err != nil {
				t.Fatal(err)
			}
		}
		return codexProbeTestText(codexProbeTestNonce(t, body)), nil
	})
	result := ProbeCodexCapability(context.Background(), a, CodexCapabilityProbeOptions{Model: "gpt-6-astra", Level: "tools"})
	if calls != 1 || result.Outcome != "blocked" || result.ErrorCode != "basispoints_model_not_selected" {
		t.Fatalf("revoked model used again: calls=%d result=%+v", calls, result)
	}
}
