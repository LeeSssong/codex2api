package proxy

import (
	"context"
	"errors"
	"fmt"
	"github.com/tidwall/gjson"
	"image/color"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
)

func newBasispointsPolicyRouteAccount(t *testing.T) (*database.DB, *auth.Account) {
	t.Helper()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "route.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	id, err := db.InsertAccount(context.Background(), "route-test", "synthetic-refresh", "")
	if err != nil {
		t.Fatal(err)
	}
	s := auth.NewStore(db, cache.NewMemory(8), &database.SystemSettings{MaxConcurrency: 2})
	t.Cleanup(s.Stop)
	if err = s.LoadAccountByID(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	a := s.FindByID(id)
	a.Mu().Lock()
	a.AccessToken = "synthetic-access"
	a.AccountID = "workspace"
	a.Mu().Unlock()
	return db, a
}

func TestBasispointsAccountEmptySelectionCannotReachUpstream(t *testing.T) {
	enableBasispointsForTest(t)
	db, a := newBasispointsPolicyRouteAccount(t)
	if err := db.SetBasispointsAccountPolicy(context.Background(), a.ID(), database.BasispointsAccountPolicy{ModelScope: "selected", Models: []string{}}); err != nil {
		t.Fatal(err)
	}
	if err := a.ReloadCodexRoutes(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := newCodexRouteDecision(context.Background(), "alias", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_only"}, 3)
	if d.pathEligible(a, database.CodexPathBasispoints, "gpt-6-astra", nil) {
		t.Fatal("empty selected model set was treated as all")
	}
}

func TestBasispointsHTTP403ClosesOnlyOriginalAccountPolicy(t *testing.T) {
	for _, manualEdit := range []bool{false, true} {
		t.Run(map[bool]string{false: "auto_close", true: "new_manual_policy_wins"}[manualEdit], func(t *testing.T) {
			enableBasispointsForTest(t)
			db, a := newBasispointsPolicyRouteAccount(t)
			ctx := context.Background()
			if err := db.SetBasispointsAccountPolicy(ctx, a.ID(), database.BasispointsAccountPolicy{ModelScope: "all", AutoDisableOn403: true}); err != nil {
				t.Fatal(err)
			}
			if err := a.ReloadCodexRoutes(ctx); err != nil {
				t.Fatal(err)
			}
			calls := 0
			installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
				calls++
				if manualEdit {
					if err := db.SetCodexPathAllowed(ctx, a.ID(), "basispoints", true); err != nil {
						t.Fatal(err)
					}
				}
				return routeTestResponse(403, "<html>Forbidden</html>"), nil
			})
			nativeCalls := 0
			native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
				nativeCalls++
				return routeTestResponse(200, ""), nil
			}
			d := newCodexRouteDecision(ctx, "gpt-6-astra", "gpt-6-astra", database.APIKeyLimits{CodexRoutePolicy: "basispoints_only"}, 3)
			requestCtx := context.WithValue(ctx, codexRouteKey{}, d)
			resp, err := executeCodexRoute(requestCtx, a, []byte("{\"model\":\"gpt-6-astra\",\"input\":\"hello\"}"), "", "", "key", nil, nil, false, native)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			p, err := db.GetBasispointsAccountPolicy(ctx, a.ID())
			if err != nil {
				t.Fatal(err)
			}
			if (p.DisabledBy == "auto_403") == manualEdit {
				t.Fatalf("CAS policy outcome incorrect: %+v", p)
			}
			if calls != 1 || nativeCalls != 0 || a.Disabled != 0 || !a.CodexPathSnapshot("codex", "gpt-6-astra", time.Now()).Allowed {
				t.Fatal("account or strict route boundary crossed")
			}
		})
	}
}

func TestBasispointsImageCapacityNeverFallsBack(t *testing.T) {
	enableBasispointsForTest(t)
	host := &fakeImageHost{err: errors.New("private storage failure")}
	host.install(t)
	account := &auth.Account{DBID: 9901, AccountID: "workspace", AccessToken: "test-token"}
	nativeCalls := 0
	native := func(context.Context, *auth.Account, []byte, string, string, string, *DeviceProfileConfig, http.Header) (*http.Response, error) {
		nativeCalls++
		return routeTestResponse(200, ""), nil
	}
	body := []byte(fmt.Sprintf("{\"model\":\"gpt-6-astra\",\"input\":[{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"image_url\":%q}]}]}", dataURL(solidPNG(t, 4, color.RGBA{A: 255}))))
	_, err := executeCodexRoute(context.Background(), account, body, "", "", "key", nil, nil, false, native)
	var failure *Error
	if !errors.As(err, &failure) || failure.HTTPStatus != 503 || failure.Code != "image_capacity_exhausted" || nativeCalls != 0 || failure.Retryable {
		t.Fatalf("capacity must terminate without upstream dispatch: calls=%d err=%v", nativeCalls, err)
	}
}
func TestBasispointsActualBridgeHonorsCachePolicySnapshot(t *testing.T) {
	for _, asInput := range []bool{false, true} {
		t.Run(fmt.Sprint(asInput), func(t *testing.T) {
			enableBasispointsForTest(t)
			db, a := newBasispointsPolicyRouteAccount(t)
			p := database.BasispointsAccountPolicy{ModelScope: "all", CacheCreationAsInput: asInput}
			if err := db.SetBasispointsAccountPolicy(context.Background(), a.ID(), p); err != nil {
				t.Fatal(err)
			}
			if err := a.ReloadCodexRoutes(context.Background()); err != nil {
				t.Fatal(err)
			}
			installBasispointsTransport(t, a, func(*http.Request) (*http.Response, error) {
				p.CacheCreationAsInput = !asInput
				if err := db.SetBasispointsAccountPolicy(context.Background(), a.ID(), p); err != nil {
					t.Fatal(err)
				}
				return routeTestResponse(200, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_billing\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1000,\"output_tokens\":50,\"total_tokens\":1050,\"input_tokens_details\":{\"cached_tokens\":100},\"cache_creation_input_tokens\":200}}}\n\ndata: [DONE]\n\n"), nil
			})
			resp, err := ExecuteRequest(context.Background(), a, []byte("{\"model\":\"gpt-6-astra\",\"input\":\"hi\"}"), "", "", "key", nil, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			want := 200
			if asInput {
				want = 0
			}
			found := false
			for _, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(line, "data: ") {
					v := gjson.Parse(strings.TrimPrefix(line, "data: "))
					if v.Get("type").String() == "response.completed" {
						found = true
						if v.Get("response.usage.cache_creation_input_tokens").Int() != int64(want) || v.Get("response.usage.input_tokens").Int() != 1000 {
							t.Fatalf("dispatch policy ignored: %s", line)
						}
					}
				}
			}
			if !found {
				t.Fatalf("missing completed usage: %s", body)
			}
		})
	}
}
func TestBasispointsPersistedSettingsOverrideEnvironmentAndFailClosed(t *testing.T) {
	previous := basispointsSettingsSnapshot.Load()
	runtime := CurrentRuntimeSettings()
	t.Cleanup(func() { basispointsSettingsSnapshot.Store(previous); ApplyRuntimeSettings(runtime) })
	t.Setenv("BASISPOINTS_MODELS", "*")
	db, _ := newBasispointsPolicyRouteAccount(t)
	s := database.DefaultBasispointsSettings()
	s.Enabled = true
	s.ModelScope = "selected"
	s.Models = []string{}
	if err := db.SaveBasispointsSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := SyncBasispointsSettings(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	if basispointsModelAllowed("gpt-6-astra") {
		t.Fatal("saved empty selection lost to env")
	}
	s.Models = []string{"gpt-6-astra"}
	if err := db.SaveBasispointsSettings(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := SyncBasispointsSettings(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	if !basispointsActiveForModel("gpt-6-astra") {
		t.Fatal("saved model not applied")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := SyncBasispointsSettings(ctx, db, nil); err == nil || CurrentRuntimeSettings().CodexBasispointsEnabled {
		t.Fatal("configuration failure must close BPS")
	}
}

func TestBasispointsRecoverySuggestionRequiresObservedFailure(t *testing.T) {
	current := CodexProbeResult{Model: "gpt-6-astra", Level: "basic"}
	old := database.CodexProbeResult{Upstream: "basispoints", Model: "gpt-6-astra", Level: "basic", Outcome: "blocked", Attempts: 1}
	if !basispointsProbeRecoverySuggested([]database.CodexProbeResult{old}, current) {
		t.Fatal("observed failure should suggest review after success")
	}
	old.Attempts = 0
	if basispointsProbeRecoverySuggested([]database.CodexProbeResult{old}, current) {
		t.Fatal("a locally blocked test is not a recovery observation")
	}
}
