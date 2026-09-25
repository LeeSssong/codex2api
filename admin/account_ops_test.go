package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/codex2api/accountops"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountOpsModuleDefaultDisabledAndSMTPRedaction(t *testing.T) {
	db, e := database.New("sqlite", filepath.Join(t.TempDir(), "ops.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	h := &Handler{db: db}
	ctx, cancel := context.WithCancel(context.Background())
	h.StartAccountOps(ctx)
	defer func() { cancel(); h.WaitAccountOps() }()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/account-ops/module", nil)
	h.GetAccountOpsModule(c)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatal(w.Body.String())
	}
	if e = db.SaveAccountOpsSMTP(ctx, accountops.SMTPConfig{Host: "smtp.example.test", Port: 587, From: "from@example.test", TLSMode: "starttls", Password: "test-smtp-secret"}); e != nil {
		t.Fatal(e)
	}
	redacted := httptest.NewRecorder()
	rc, _ := gin.CreateTestContext(redacted)
	rc.Request = httptest.NewRequest(http.MethodGet, "/account-ops/config", nil)
	h.GetAccountOpsConfig(rc)
	if redacted.Code != 200 || strings.Contains(redacted.Body.String(), "test-smtp-secret") || !strings.Contains(redacted.Body.String(), `"password_configured":true`) {
		t.Fatalf("SMTP secret redaction failed: %d", redacted.Code)
	}
	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/quality-ops/plans", strings.NewReader(`{}`))
	h.SaveAccountQualityPlan(c)
	if w.Code != 409 {
		t.Fatalf("disabled module accepted mutation %d", w.Code)
	}
}
func TestAccountOpsTextPayloadDoesNotInstructHTML(t *testing.T) {
	body, e := buildQualityTestPayload(&auth.Account{}, "model", qualityTestRequest{Prompt: "question", TextOnly: true}, auth.ClaudeSecurityConfig{})
	if e != nil || strings.Contains(string(body), "HTML") {
		t.Fatalf("text payload %s %v", body, e)
	}
}

func TestAccountOpsParallelRoundAndCancellation(t *testing.T) {
	for _, cancelJob := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelJob), func(t *testing.T) {
			ctx := context.Background()
			db := newTestAdminDB(t)
			store := auth.NewStore(db, nil, nil)
			defer store.Stop()
			h := &Handler{db: db, store: store, accountOps: &accountOpsRuntime{}}
			h.accountOps.enabled.Store(true)
			h.accountOps.alerts = accountops.NewAccountOpsService(database.NewAccountOpsSettings(db), database.NewAccountOpsRepository(db), nil)
			cfg := accountops.DefaultConfig()
			cfg.Enabled, cfg.Recipient, cfg.QualityDegraded = true, "ops@example.com", true
			if e := h.accountOps.alerts.SaveConfig(ctx, cfg); e != nil {
				t.Fatal(e)
			}
			h.accountOps.alerts.Start()
			defer h.accountOps.alerts.Stop()
			arrived := make(chan struct{}, 4)
			release := make(chan struct{})
			var judgeCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				answer := "21个。"
				if strings.Contains(string(raw), "candidate_answer") {
					judgeCalls.Add(1)
					if r.Header.Get("Authorization") == "Bearer tested" {
						t.Error("tested account graded itself")
					}
					if !strings.Contains(string(raw), `"effort":"medium"`) {
						t.Error("judge did not use source medium effort")
					}
					answer = `{"verdict":"incorrect","reason":"wrong conclusion"}`
				} else {
					if !strings.Contains(string(raw), "只输出最终答案，不要解释。") {
						t.Error("missing source text contract")
					}
					arrived <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				encoded, _ := json.Marshal(answer)
				fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", encoded)
			}))
			defer upstream.Close()
			ids := []int64{}
			for _, key := range []string{"tested", "judge"} {
				id, e := db.InsertAccountWithCredentials(ctx, key, map[string]any{"upstream_type": "openai_responses", "api_key": key, "models": []string{"gpt-4o-mini"}, "base_url": upstream.URL}, "")
				if e != nil {
					t.Fatal(e)
				}
				ids = append(ids, id)
				store.AddAccount(&auth.Account{DBID: id, UpstreamType: auth.UpstreamOpenAIResponses, APIKey: key, BaseURL: upstream.URL, Models: []string{"gpt-4o-mini"}, PlanType: "pro", Status: auth.StatusReady})
			}
			group, e := db.CreateAccountGroup(ctx, "judges", "", "", 0, 0, sql.NullInt64{})
			if e != nil {
				t.Fatal(e)
			}
			for _, id := range ids {
				if e = db.SetAccountGroups(ctx, id, []int64{group}); e != nil {
					t.Fatal(e)
				}
				store.ApplyAccountGroups(id, []int64{group})
			}
			p, e := db.SaveAccountQualityPlan(ctx, accountops.Plan{AccountID: ids[0], Enabled: true, Model: "gpt-4o-mini", Prompt: "question", ReasoningEffort: "high", Cron: "* * * * *", Samples: 2, ExpectedAnswer: "21", Action: "disable_scheduling", Judge: &accountops.JudgeConfig{GroupID: group, ModelID: "gpt-4o-mini", Prompt: "grade"}})
			if e != nil {
				t.Fatal(e)
			}
			if e = db.TriggerAccountQualityPlan(ctx, p.ID); e != nil {
				t.Fatal(e)
			}
			claimed, e := db.ClaimAccountQualityPlan(ctx, time.Now())
			if e != nil || claimed == nil {
				t.Fatal(e)
			}
			done := make(chan struct{})
			go func() { defer close(done); h.runAccountQualityRound(ctx, *claimed) }()
			for i := 0; i < 2; i++ {
				select {
				case <-arrived:
				case <-time.After(5 * time.Second):
					close(release)
					t.Fatal("samples were not parallel")
				}
			}
			if cancelJob {
				jobs, e := db.ListQualityTests(ctx, 1, 20, database.QualityTestFilter{})
				if e != nil || len(jobs.ActiveJobs) != 1 {
					t.Fatal("native job not admitted")
				}
				if e = db.CancelQualityTest(ctx, jobs.ActiveJobs[0].ID); e != nil {
					t.Fatal(e)
				}
			} else {
				close(release)
			}
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				if cancelJob {
					close(release)
				}
				t.Fatal("runner did not finish")
			}
			if cancelJob {
				close(release)
			}
			row, e := db.GetAccountByID(ctx, ids[0])
			if e != nil {
				t.Fatal(e)
			}
			rounds, e := db.ListAccountQualityHistory(ctx, 0, true)
			if e != nil || len(rounds) != 1 {
				t.Fatalf("round missing %v", e)
			}
			jobs, e := db.ListQualityTests(ctx, 1, 20, database.QualityTestFilter{})
			if e != nil || len(jobs.ActiveJobs) != 0 {
				t.Fatal("capacity leaked")
			}
			if cancelJob {
				if !row.Enabled || rounds[0].Action != "cancelled" || jobs.Jobs[0].Status != "stopped" || judgeCalls.Load() != 0 {
					t.Fatalf("cancel action %+v job %+v enabled %v calls %d", rounds[0], jobs.Jobs[0], row.Enabled, judgeCalls.Load())
				}
				alerts, e := database.NewAccountOpsRepository(db).List(ctx, 0, 10)
				if e != nil || len(alerts) != 0 {
					t.Fatal("cancelled round queued a quality alert")
				}
			} else {
				if row.Enabled || rounds[0].Action != "scheduling_disabled" || judgeCalls.Load() != 2 {
					t.Fatalf("wrong answer not applied %+v enabled=%v calls=%d", rounds[0], row.Enabled, judgeCalls.Load())
				}
				require.Eventually(t, func() bool {
					alerts, e := database.NewAccountOpsRepository(db).List(ctx, 0, 10)
					return e == nil && len(alerts) == 1 && alerts[0].Kind == "quality_degraded"
				}, time.Second*2, time.Millisecond*20)
			}
		})
	}
}

func TestAccountOpsWhitespaceAndIncompleteAnswersAreUnknown(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		complete     bool
	}{{"whitespace", " \n\t", true}, {"incomplete", "21", false}} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestAdminDB(t)
			store := auth.NewStore(db, nil, nil)
			defer store.Stop()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				body, _ := json.Marshal(tc.answer)
				fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\n", body)
				if tc.complete {
					fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
				}
			}))
			defer upstream.Close()
			id, e := db.InsertAccountWithCredentials(context.Background(), "test", map[string]any{"upstream_type": "openai_responses", "api_key": "test", "base_url": upstream.URL, "models": []string{"gpt-4o-mini"}}, "")
			if e != nil {
				t.Fatal(e)
			}
			store.AddAccount(&auth.Account{DBID: id, UpstreamType: auth.UpstreamOpenAIResponses, APIKey: "test", BaseURL: upstream.URL, Models: []string{"gpt-4o-mini"}, Status: auth.StatusReady})
			h := &Handler{db: db, store: store}
			if _, e = h.runAccountOpsText(context.Background(), id, "gpt-4o-mini", "q", "medium"); e == nil {
				t.Fatal("inconclusive answer accepted")
			}
		})
	}
}
func TestAccountOpsAntigravityJudgeUsesEncodedEffort(t *testing.T) {
	ctx := context.Background()
	db := newTestAdminDB(t)
	store := auth.NewStore(db, nil, nil)
	defer store.Stop()
	id, e := db.InsertAccountWithCredentials(ctx, "judge", map[string]any{"upstream_type": "antigravity", "access_token": "google-test", "project_id": "project-test"}, "")
	if e != nil {
		t.Fatal(e)
	}
	account := newAntigravityConnectionTestAccount()
	account.DBID = id
	store.AddAccount(account)
	group, e := db.CreateAccountGroup(ctx, "judges", "", "", 0, 0, sql.NullInt64{})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.SetAccountGroups(ctx, id, []int64{group}); e != nil {
		t.Fatal(e)
	}
	store.ApplyAccountGroups(id, []int64{group})
	h := &Handler{db: db, store: store}
	calls := 0
	h.antigravityCapabilityProbe = func(_ context.Context, _ *auth.Account, model string, body []byte, stream bool, _ string) (*http.Response, error) {
		calls++
		if strings.Contains(string(body), `"effort"`) {
			t.Error("encoded Gemini model received separate effort")
		}
		answer, _ := json.Marshal(`{"verdict":"correct","reason":"equivalent"}`)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", answer)))}, nil
	}
	p := accountops.Plan{AccountID: 999, Prompt: "q", ExpectedAnswer: "21", Judge: &accountops.JudgeConfig{GroupID: group, ModelID: "gemini-3.5-flash-low", Prompt: "grade"}}
	judgment := h.judgeAccountQuality(ctx, p, "21")
	if calls != 1 || judgment.Verdict != "correct" {
		t.Fatalf("judge not reached %d %+v", calls, judgment)
	}
}
