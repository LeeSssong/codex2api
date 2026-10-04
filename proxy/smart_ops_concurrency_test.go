package proxy

import (
	"context"
	"database/sql"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/smartops"
	"github.com/gin-gonic/gin"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConcurrencyRequestFailureDominatesRetry(t *testing.T) {
	var got []smartops.ConcurrencyObservation
	a := &concurrencyRequestAudit{entries: map[int64]*concurrencyRequestResult{}, report: func(o smartops.ConcurrencyObservation) { got = append(got, o) }}
	a.begin(smartops.ConcurrencyObservation{AccountID: 1})
	a.result(&database.UsageLogInput{AccountID: 1, StatusCode: 502})
	a.result(&database.UsageLogInput{AccountID: 1, StatusCode: 200})
	a.begin(smartops.ConcurrencyObservation{AccountID: 2})
	a.result(&database.UsageLogInput{AccountID: 2, StatusCode: 200})
	a.finish(context.Background())
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	for _, o := range got {
		if o.Success != (o.AccountID == 2) {
			t.Fatalf("wrong result %+v", o)
		}
	}
	a.finish(context.Background())
	if len(got) != 2 {
		t.Fatal("double finalization counted twice")
	}
}

func TestConcurrencyRequestCancelAndMissingCompletion(t *testing.T) {
	var got []smartops.ConcurrencyObservation
	a := &concurrencyRequestAudit{entries: map[int64]*concurrencyRequestResult{}, report: func(o smartops.ConcurrencyObservation) { got = append(got, o) }}
	a.begin(smartops.ConcurrencyObservation{AccountID: 1})
	a.result(&database.UsageLogInput{AccountID: 1, StatusCode: 200})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a.finish(ctx)
	if len(got) != 0 {
		t.Fatal("cancelled success counted")
	}
	a = &concurrencyRequestAudit{entries: map[int64]*concurrencyRequestResult{}, report: func(o smartops.ConcurrencyObservation) { got = append(got, o) }}
	a.begin(smartops.ConcurrencyObservation{AccountID: 1})
	a.finish(context.Background())
	if len(got) != 1 || got[0].Success {
		t.Fatal("missing completed outcome counted as success")
	}
}

func TestConcurrencyRequestScopeExclusions(t *testing.T) {
	for _, tc := range []struct {
		path, body string
		want       bool
	}{
		{"/v1/responses", `{"model":"gpt-5"}`, true},
		{"/v1/chat/completions", `{}`, true},
		{"/v1/messages", `{}`, true},
		{"/v1beta/models/gemini:streamGenerateContent", `{}`, true},
		{"/v1beta/models/gemini:countTokens", `{}`, false},
		{"/v1/responses/compact", `{}`, false},
		{"/v1/images/generations", `{}`, false},
		{"/v1/responses", `{"tool_choice":{"type":"image_generation"}}`, false},
		{"/v1/responses", `{"model":"gpt-5.5","input":"Generate an image of a cat"}`, false},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
		if got := concurrencyRequestEligible(c, []byte(tc.body)); got != tc.want {
			t.Errorf("%s got %v", tc.path, got)
		}
	}
}

func TestConcurrencyRequestExcludedUsageDoesNotCount(t *testing.T) {
	for _, in := range []*database.UsageLogInput{
		{AccountID: 1, StatusCode: 200, ViaWebsocket: true},
		{AccountID: 1, StatusCode: 200, InternalReason: "probe"},
		{AccountID: 1, StatusCode: 200, Compact: true},
		{AccountID: 1, StatusCode: 200, ImageCount: 1},
	} {
		var got []smartops.ConcurrencyObservation
		a := &concurrencyRequestAudit{entries: map[int64]*concurrencyRequestResult{}, report: func(o smartops.ConcurrencyObservation) { got = append(got, o) }}
		a.begin(smartops.ConcurrencyObservation{AccountID: 1})
		a.result(in)
		a.finish(context.Background())
		if len(got) != 0 {
			t.Fatalf("excluded usage counted %+v", in)
		}
	}
}

func TestConcurrencyRequestTransportFailureOverridesSuccessLog(t *testing.T) {
	var got []smartops.ConcurrencyObservation
	a := &concurrencyRequestAudit{entries: map[int64]*concurrencyRequestResult{}, report: func(o smartops.ConcurrencyObservation) { got = append(got, o) }}
	a.begin(smartops.ConcurrencyObservation{AccountID: 1})
	a.transportResult(1, &http.Response{StatusCode: 429}, nil)
	a.result(&database.UsageLogInput{AccountID: 1, StatusCode: 200})
	a.finish(context.Background())
	if len(got) != 1 || got[0].Success {
		t.Fatal("hidden HTTP retry failure counted as success")
	}
}

func TestConcurrencyRequestDeliveryFailureDoesNotCountSuccess(t *testing.T) {
	var got []smartops.ConcurrencyObservation
	a := &concurrencyRequestAudit{entries: map[int64]*concurrencyRequestResult{}, report: func(o smartops.ConcurrencyObservation) { got = append(got, o) }}
	a.begin(smartops.ConcurrencyObservation{AccountID: 1})
	a.result(&database.UsageLogInput{AccountID: 1, StatusCode: 200})
	w := &concurrencyDeliveryWriter{audit: a}
	if _, err := w.record(1, 2, nil); err == nil {
		t.Fatal("short write must fail delivery")
	}
	a.finish(context.Background())
	if len(got) != 0 {
		t.Fatal("unsent success counted")
	}
}

func TestConcurrencyDeliveryWriterPreservesHTTPKeepaliveUnwrap(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	a := &concurrencyRequestAudit{}
	c.Writer = &concurrencyDeliveryWriter{ResponseWriter: c.Writer, audit: a}
	raw, ok := unwrapHTTPResponseWriter(c.Writer)
	if !ok || raw != recorder {
		t.Fatal("concurrency wrapper disabled HTTP informational keepalive")
	}
}

func TestConcurrencyHTTPResponsesCompleteAndTruncatedStream(t *testing.T) {
	for _, completed := range []bool{true, false} {
		t.Run(map[bool]string{true: "completed", false: "truncated"}[completed], func(t *testing.T) {
			db, err := database.New("sqlite", filepath.Join(t.TempDir(), "concurrency.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			group, err := db.CreateAccountGroup(context.Background(), "managed", "", "#345678", 0, 0, sql.NullInt64{})
			if err != nil {
				t.Fatal(err)
			}
			cfg := smartops.DefaultOAuthAutoConfig()
			cfg.UpgradeEnabled = true
			cfg.UpgradeGroupIDs = []int64{group}
			cfg.Revision = "test"
			cfg.SuccessesPerStep = 20
			if err = db.SaveOAuthAutoConfig(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			id, err := db.InsertAccountWithCredentials(context.Background(), "test", map[string]interface{}{"access_token": "synthetic"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err = db.SetAccountGroups(context.Background(), id, []int64{group}); err != nil {
				t.Fatal(err)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
				if completed {
					io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
				}
			}))
			defer upstream.Close()
			store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 2, MaxRetries: 0, MaxRateLimitRetries: 0})
			store.AddAccount(&auth.Account{DBID: id, UpstreamType: auth.UpstreamOpenAIResponses, BaseURL: upstream.URL, APIKey: "sk-test", Models: []string{"gpt-5.5"}, PlanType: "api"})
			ctx, cancel := context.WithCancel(context.Background())
			defer func() { cancel(); store.WaitSmartOps() }()
			store.StartSmartOps(ctx, func(context.Context, string) bool { return true }, func(context.Context) (smartops.OAuthAutoConfig, smartops.PriorityConfig, error) {
				return cfg, smartops.DefaultPriorityConfig(), nil
			})
			deadline := time.Now().Add(time.Second)
			for store.BeginSmartOpsConcurrencyObservation(store.FindByID(id)).AccountID == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			handler := NewHandler(store, db, nil, nil)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello","stream":true}`))
			handler.Responses(c)
			deadline = time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				progress, e := db.LoadSmartOpsConcurrencyProgress(context.Background(), []int64{id}, cfg)
				state := progress[id]
				if e == nil && ((completed && state.Successes == 1) || (!completed && !state.PausedUntil.IsZero())) {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			paused, reason := store.SmartOpsConcurrencyStatus()
			t.Fatalf("HTTP outcome did not reach persisted progression paused=%v reason=%s", paused, reason)
		})
	}
}
