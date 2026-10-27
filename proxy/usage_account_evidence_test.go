package proxy

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// These fixtures fail if diagnostic attempts become quota requests, or if
// canceled/partial observed usage is discarded by a status/retry filter.
func TestAccountUsageAdmissionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		usage  *UsageInfo
		result bool
		status int
		retry  bool
		reason string
		apiKey int64
		want   bool
		tokens int64
	}{
		{name: "success zero audit", result: true, status: 200, want: true},
		{name: "transport absent result", status: 598},
		{name: "pre-response rejected", status: 429},
		{name: "cache category exceeds total", usage: &UsageInfo{InputTokens: 0, CachedTokens: 20, OutputTokens: 10}, status: 499, want: true, tokens: 30},
		{name: "partial cancellation", usage: &UsageInfo{InputTokens: 1000, OutputTokens: 100, CachedTokens: 200}, status: 499, want: true, tokens: 1100},
		{name: "observed retry", usage: &UsageInfo{InputTokens: 20, OutputTokens: 3}, status: 502, retry: true, want: true, tokens: 23},
		{name: "retry no result", status: 502, retry: true},
		{name: "connection probe", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "connection_test"},
		{name: "quality probe", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "quality_test"},
		{name: "capability probe", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "grok_capability_probe"},
		{name: "intelligence probe", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "intelligence_check"},
		{name: "unknown internal traffic", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "future_probe"},
		{name: "unattributed child", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "overflow_compact_summary"},
		{name: "attributed compact child", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "overflow_compact_summary", apiKey: 1, want: true, tokens: 20},
		{name: "attributed analysis", usage: &UsageInfo{InputTokens: 20}, result: true, status: 200, reason: "prompt_intelligence_cy_analysis", apiKey: 1, want: true, tokens: 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &database.UsageLogInput{StatusCode: tc.status, IsRetryAttempt: tc.retry, InternalReason: tc.reason, APIKeyID: tc.apiKey, InputTokens: 9999, TotalTokens: 9999}
			observation := observeAccountUsage(tc.usage, tc.result)
			observation.apply(input)
			finalizeAccountUsageEvidence(input)
			if input.QuotaUsageAdmitted == nil || *input.QuotaUsageAdmitted != tc.want {
				t.Fatalf("admitted=%v want %v", input.QuotaUsageAdmitted, tc.want)
			}
			if input.QuotaTokens == nil || *input.QuotaTokens != tc.tokens {
				t.Fatalf("tokens=%v want %d", input.QuotaTokens, tc.tokens)
			}
			if input.InputTokens != 9999 {
				t.Fatal("account evidence modified legacy user usage")
			}
		})
	}
}

func TestAccountUsageEvidenceFreezesObservedBeforeEstimate(t *testing.T) {
	observed := &UsageInfo{InputTokens: 1000, OutputTokens: 100, CachedTokens: 200, CacheWriteTokens: 50}
	observation := observeAccountUsage(observed, true)
	observed.InputTokens = 7000
	observed.OutputTokens = 8000
	input := &database.UsageLogInput{InputTokens: 0, OutputTokens: 999, TotalTokens: 999, StatusCode: 499}
	observation.apply(input)
	finalizeAccountUsageEvidence(input)
	if input.AccountUsage.InputTokens != 1000 || input.AccountUsage.OutputTokens != 100 || input.AccountUsage.CacheWrite5mTokens != 50 {
		t.Fatalf("original usage lost: %+v", input.AccountUsage)
	}
	if *input.QuotaTokens != 1100 || input.OutputTokens != 999 {
		t.Fatalf("account/user separation failed: %+v", input)
	}
}

func TestAccountUsageUnknownOutputDoesNotPriceEstimatedTokens(t *testing.T) {
	input := &database.UsageLogInput{OutputTokens: 333, TotalTokens: 333, StatusCode: 598}
	observeAccountUsage(nil, true).apply(input)
	finalizeAccountUsageEvidence(input)
	if !*input.QuotaUsageAdmitted || *input.QuotaTokens != 0 || input.AccountUsage.OutputTokens != 0 {
		t.Fatalf("estimated output became observed account usage: %+v", input)
	}
}

func TestAccountUsageExtractCacheWritesFromObservedResponse(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{`{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cache_write_tokens":50},"cache_write_tokens":99}`, 50},
		{`{"input_tokens":1000,"input_tokens_details":{"cache_write_tokens":0},"cache_write_tokens":99}`, 0},
		{`{"input_tokens":1000,"cache_creation_input_tokens":60}`, 60},
	} {
		observed := extractUsageFromResult(gjson.Parse(tc.raw))
		if observed == nil {
			t.Fatal("observed usage missing")
		}
		evidence := observeAccountUsage(observed, true)
		if evidence.usage.CacheWrite5mTokens != tc.want {
			t.Fatalf("cache write evidence=%+v want %d", evidence, tc.want)
		}
		if observed.CacheWriteTokens != 0 {
			t.Fatal("native billing cache write behavior changed")
		}
	}
}

func TestAccountUsageCommonSinkPersistsObservedCostAndAdmission(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetUsageLogConfig(database.UsageLogModeFull, 1, 1)
	h := &Handler{db: db}
	input := &database.UsageLogInput{AccountID: 1, Model: "gpt-5.4", StatusCode: 502, InputTokens: 0, OutputTokens: 999, TotalTokens: 999}
	observeAccountUsage(&UsageInfo{InputTokens: 1000, OutputTokens: 100, CachedTokens: 200}, true).apply(input)
	h.logUsage(input)
	logs := waitAccountEvidenceLogs(t, db, 1)
	got := logs[0]
	if got.QuotaUsageAdmitted == nil || !*got.QuotaUsageAdmitted || got.QuotaTokens == nil || *got.QuotaTokens != 1100 {
		t.Fatalf("persisted evidence=%+v", got)
	}
	if got.AccountCost == nil || math.Abs(*got.AccountCost-.00355) > 1e-10 {
		t.Fatalf("original account fee=%v want .00355", got.AccountCost)
	}
	if got.OutputTokens != 999 {
		t.Fatal("legacy output estimate was changed")
	}
}

func TestAccountUsageFailedStreamPreservesActualTokensBeforeLegacyEstimate(t *testing.T) {
	handler, _ := newChatStreamTerminalTestHandler(t, []string{
		`{"type":"response.created","response":{"id":"partial_cost"}}`,
		`{"type":"response.output_text.delta","delta":"partial visible output"}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"stream failed"},"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200}}}}`,
	})
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "partial.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetUsageLogConfig(database.UsageLogModeFull, 1, 1)
	handler.db = db
	invokeChatCompletionsStream(t, handler)
	logs := waitAccountEvidenceLogs(t, db, 1)
	got := logs[0]
	if got.QuotaTokens == nil || *got.QuotaTokens != 1100 || got.QuotaUsageAdmitted == nil || !*got.QuotaUsageAdmitted {
		t.Fatalf("observed quota evidence=%+v", got)
	}
	if got.AccountCost == nil || math.Abs(*got.AccountCost-.0071) > 1e-10 {
		t.Fatalf("account fee=%v want .0071", got.AccountCost)
	}
	if got.OutputTokens == 100 {
		t.Fatal("test must exercise legacy estimated output replacement")
	}
}

func TestAccountUsageFailedStreamUnknownDoesNotChargeCharacterEstimate(t *testing.T) {
	handler, _ := newChatStreamTerminalTestHandler(t, []string{
		`{"type":"response.created","response":{"id":"unknown_cost"}}`,
		`{"type":"response.output_text.delta","delta":"partial visible output"}`,
	})
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "unknown.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetUsageLogConfig(database.UsageLogModeFull, 1, 1)
	handler.db = db
	invokeChatCompletionsStream(t, handler)
	logs := waitAccountEvidenceLogs(t, db, 1)
	got := logs[0]
	if got.QuotaTokens == nil || *got.QuotaTokens != 0 {
		t.Fatalf("unknown token quantity=%+v", got)
	}
	if got.AccountCost == nil || *got.AccountCost != 0 {
		t.Fatalf("character estimate charged account=%v", got.AccountCost)
	}
	if got.OutputTokens <= 0 {
		t.Fatal("legacy estimate was lost")
	}
}

func TestAccountUsageFailureTerminalSnapshotOverridesEarlierObservedUsage(t *testing.T) {
	earlier := &UsageInfo{InputTokens: 10, OutputTokens: 1}
	got := observedAttemptUsage(earlier, []byte(`{"type":"response.failed","response":{"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200}}}}`))
	if got == nil || got.InputTokens != 1000 || got.OutputTokens != 100 || got.CachedTokens != 200 {
		t.Fatalf("failed terminal usage=%+v", got)
	}
	if earlier.InputTokens != 10 {
		t.Fatal("legacy usage mutated")
	}
}

func waitAccountEvidenceLogs(t *testing.T, db *database.DB, want int) []*database.UsageLog {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		db.FlushUsageLogs()
		logs, err := db.ListRecentUsageLogs(context.Background(), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(logs) == want {
			return logs
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs=%d want%d", len(logs), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAccountUsageObservedRetryAndSuccessAreRecordedOnceEach(t *testing.T) {
	var attempts atomic.Int32
	handler, _ := newChatStreamServeTestHandler(t, func(w http.ResponseWriter) {
		if attempts.Add(1) == 1 {
			_, _ = io.WriteString(w, "data: "+`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"retry partial"},"usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200}}}}`+"\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: "+`{"type":"response.output_text.delta","delta":"ok"}`+"\n\n")
		_, _ = io.WriteString(w, "data: "+`{"type":"response.completed","response":{"id":"retry_completed","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":10,"output_tokens":2}}}`+"\n\n")
	})
	handler.store.AddAccount(&auth.Account{DBID: 2, AccessToken: "at-2", PlanType: "pro", AccountID: "acct-2"})
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "retry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetUsageLogConfig(database.UsageLogModeFull, 1, 1)
	handler.db = db
	invokeChatCompletionsStream(t, handler)
	logs := waitAccountEvidenceLogs(t, db, 2)
	if attempts.Load() != 2 {
		t.Fatalf("upstream attempts=%d", attempts.Load())
	}
	var partial, success int
	for _, got := range logs {
		if got.QuotaUsageAdmitted == nil || !*got.QuotaUsageAdmitted {
			t.Fatalf("observed attempt excluded: %+v", got)
		}
		if got.IsRetryAttempt {
			partial++
			if got.TotalTokens != 0 || got.UserBilled != 0 || got.AccountBilled != 0 {
				t.Fatalf("new account-only retry altered native counters: %+v", got)
			}
			if got.QuotaTokens == nil || *got.QuotaTokens != 1100 || got.AccountCost == nil || math.Abs(*got.AccountCost-.0071) > 1e-10 {
				t.Fatalf("partial retry evidence=%+v", got)
			}
		} else {
			success++
			if got.QuotaTokens == nil || *got.QuotaTokens != 12 || got.AccountCost == nil || math.Abs(*got.AccountCost-.00011) > 1e-10 {
				t.Fatalf("success evidence=%+v", got)
			}
		}
	}
	if partial != 1 || success != 1 {
		t.Fatalf("partial=%d success=%d", partial, success)
	}
}

func TestAccountUsageDuplicateAttemptDoesNotAddQuotaTwice(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	for n, want := range []bool{true, false} {
		in := &database.UsageLogInput{AccountID: 1, RequestID: "logical", UpstreamRequestID: "attempt-a", AttemptIndex: 1, Endpoint: "/v1/responses", StatusCode: 200}
		observeAccountUsage(&UsageInfo{InputTokens: 10}, true).apply(in)
		deduplicateAccountUsageEvidence(c, in)
		if *in.QuotaUsageAdmitted != want {
			t.Fatalf("delivery%d admitted=%v", n, *in.QuotaUsageAdmitted)
		}
	}
	child := &database.UsageLogInput{AccountID: 1, RequestID: "logical", UpstreamRequestID: "attempt-b", AttemptIndex: 2, Endpoint: "/v1/responses", StatusCode: 200, ParentRequestID: "logical"}
	observeAccountUsage(&UsageInfo{InputTokens: 10}, true).apply(child)
	deduplicateAccountUsageEvidence(c, child)
	if !*child.QuotaUsageAdmitted {
		t.Fatal("separate upstream attempt or child was deduplicated")
	}
}

func TestAccountUsageFailureTierDoesNotChangeNativeUserTier(t *testing.T) {
	input := &database.UsageLogInput{Model: "gpt-5.4", BillingServiceTier: "default", StatusCode: 500}
	observeAccountUsageFromAttempt(nil, []byte(`{"response":{"service_tier":"priority","usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":200}}}}`), true).apply(input)
	if input.AccountUsage.ServiceTier == nil || *input.AccountUsage.ServiceTier != "priority" || input.BillingServiceTier != "default" {
		t.Fatalf("tier evidence=%+v native=%q", input.AccountUsage, input.BillingServiceTier)
	}
	got := database.SnapshotAccountStatsCost(input, database.AccountStatsPricingConfig{})
	if got.AccountCost == nil || math.Abs(*got.AccountCost-.0071) > 1e-10 {
		t.Fatalf("observed priority cost=%v", got.AccountCost)
	}
}

func TestAccountUsageCommonSinkInferenceUsesOriginalCategories(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  database.UsageLogInput
		want   bool
		tokens int64
	}{
		{name: "cache write only", input: database.UsageLogInput{StatusCode: 500, CacheWrite5mTokens: 20}, want: true, tokens: 20},
		{name: "image input only", input: database.UsageLogInput{StatusCode: 500, ImageInputTokens: 20}, want: true},
		{name: "diagnostic total alone", input: database.UsageLogInput{StatusCode: 598, TotalTokens: 20}},
		{name: "successful zero", input: database.UsageLogInput{StatusCode: 200}, want: true},
		{name: "http rejection", input: database.UsageLogInput{StatusCode: 429}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finalizeAccountUsageEvidence(&tc.input)
			if tc.input.QuotaUsageAdmitted == nil || *tc.input.QuotaUsageAdmitted != tc.want || *tc.input.QuotaTokens != tc.tokens {
				t.Fatalf("admitted=%v tokens=%d", *tc.input.QuotaUsageAdmitted, *tc.input.QuotaTokens)
			}
		})
	}
}

func TestAccountUsageUpstreamModelSurvivesNativeDaybreakLabel(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "model.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetUsageLogConfig(database.UsageLogModeFull, 1, 1)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set(daybreakRequestKey, daybreakRequest{Requested: "gpt-5.4-daybreak-blue", Model: "gpt-5.4", Program: "blue"})
	h := &Handler{db: db}
	h.logUsageForRequest(c, &database.UsageLogInput{AccountID: 1, Model: "client-alias", EffectiveModel: "gpt-6-astra", StatusCode: 200, InputTokens: 1000, TotalTokens: 1000})
	got := waitAccountEvidenceLogs(t, db, 1)[0]
	if got.EffectiveModel != "gpt-5.4" {
		t.Fatal("native label behavior changed")
	}
	if got.AccountCost == nil || math.Abs(*got.AccountCost-.01) > 1e-10 {
		t.Fatalf("upstream model source cost=%v want .01", got.AccountCost)
	}
}

func TestAccountUsageDownstreamWebsocketMatchesSourceAfterTurn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		websocket bool
		status    int
		image     int
		cyber     bool
		want      bool
		cached    int
	}{
		{name: "HTTP partial", status: 500, want: true, cached: 200},
		{name: "HTTP ingress upstream websocket remains HTTP", status: 499, want: true, cached: 200},
		{name: "downstream WS partial text", websocket: true, status: 500},
		{name: "downstream WS canceled text", websocket: true, status: 499},
		{name: "downstream WS image error", websocket: true, status: 500, image: 1, want: true, cached: 200},
		{name: "downstream WS cyber error", websocket: true, status: 500, cyber: true, want: true},
		{name: "downstream WS successful", websocket: true, status: 200, want: true, cached: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &database.UsageLogInput{StatusCode: tc.status, ImageCount: tc.image, ViaWebsocket: true}
			if tc.cyber {
				input.UpstreamErrorKind = "cyber_policy"
			}
			observeAccountUsage(&UsageInfo{InputTokens: 1000, OutputTokens: 100, CachedTokens: 200}, true).apply(input)
			applyAccountUsageIngressPolicy(input, tc.websocket)
			if *input.QuotaUsageAdmitted != tc.want || input.AccountUsage.CachedTokens != tc.cached && tc.want {
				t.Fatalf("admission=%v evidence=%+v", *input.QuotaUsageAdmitted, input.AccountUsage)
			}
		})
	}
}

func TestAccountUsageIngressPolicyRunsBeforeAccountFeeSink(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "ingress.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetUsageLogConfig(database.UsageLogModeFull, 1, 1)
	h := &Handler{db: db}
	for i, ws := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
		c.Set(accountUsageDownstreamWebsocketKey, ws)
		input := &database.UsageLogInput{AccountID: 1, Model: "gpt-5.4", StatusCode: 500, AttemptIndex: i + 1, InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100, CachedTokens: 200, ViaWebsocket: true}
		observeAccountUsage(&UsageInfo{InputTokens: 1000, OutputTokens: 100, CachedTokens: 200}, true).apply(input)
		h.logUsageForRequest(c, input)
	}
	logs := waitAccountEvidenceLogs(t, db, 2)
	for _, got := range logs {
		want := got.AttemptIndex == 1
		if got.QuotaUsageAdmitted == nil || *got.QuotaUsageAdmitted != want {
			t.Fatalf("ingress attempt%d admission=%v", got.AttemptIndex, got.QuotaUsageAdmitted)
		}
	}
}

func TestAccountUsageImageResultAdmittedWithoutTokenSnapshot(t *testing.T) {
	for _, ws := range []bool{false, true} {
		input := &database.UsageLogInput{StatusCode: 500, ImageCount: 1}
		observeAccountUsage(nil, false).apply(input)
		applyAccountUsageIngressPolicy(input, ws)
		if !*input.QuotaUsageAdmitted {
			t.Fatalf("actual image result lost (downstreamWS=%v)", ws)
		}
	}
}

func TestAccountUsageImageDiagnosticRemainsOutsideGatewayLedger(t *testing.T) {
	in := &database.UsageLogInput{StatusCode: 500, ImageCount: 1, InternalReason: "quality_test", UpstreamErrorKind: "cyber_policy"}
	observeAccountUsage(nil, false).apply(in)
	finalizeAccountUsageEvidence(in)
	applyAccountUsageIngressPolicy(in, true)
	if *in.QuotaUsageAdmitted {
		t.Fatal("image/cyber exception admitted diagnostic probe")
	}
}
