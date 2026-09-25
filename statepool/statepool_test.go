package statepool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
)

const correctCandy = `{"total":21,"round_count":9,"star_count":12,"guarantee_reason":"9 rounds contain apple or peach; 12 stars contain both.","minimality_reason":"All allocations with at most 20 have an adversarial draw."}`

func correctVerification() string {
	return `{"candy":` + correctCandy + `,"checks":{"a":56,"b":346,"c":14833}}`
}

func TestGradeRequiresExactAllocationAndIndependentAnswers(t *testing.T) {
	for _, tc := range []struct {
		text         string
		verify, want bool
	}{
		{correctCandy, false, true}, {correctVerification(), true, true},
		{strings.Replace(correctCandy, `"round_count":9`, `"round_count":10`, 1), false, false},
		{strings.Replace(correctCandy, `"total":21`, `"total":29`, 1), false, false},
		{strings.Replace(correctVerification(), `14833`, `14834`, 1), true, false},
		{`{"total":21}`, false, false}, {`{"total":"21"}`, false, false},
		{correctCandy + " trailing text", false, false},
	} {
		if got := Grade(tc.text, tc.verify); got != tc.want {
			t.Fatalf("grade=%v, want %v: %s", got, tc.want, tc.text)
		}
	}
}

func TestPortableRejectsExpiryRenewalAndHeaderInjection(t *testing.T) {
	now := time.Unix(10000, 0)
	valid := PortableState{MemberHash: Hash("member"), WorkspaceHash: Hash("workspace"), Model: Models[0],
		Effort: "high", Value: "opaque-original", Fingerprint: Hash("opaque-original"), CapturedAt: 9000, ExpiresAt: 12600}
	if err := ValidatePortable(valid, now); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*PortableState){
		func(s *PortableState) { s.ExpiresAt++ }, func(s *PortableState) { s.ExpiresAt = now.Unix() },
		func(s *PortableState) { s.CapturedAt = now.Unix() + 1 }, func(s *PortableState) { s.Fingerprint = Hash("other") },
		func(s *PortableState) { s.Value = "bad\r\nAuthorization: injected"; s.Fingerprint = Hash(s.Value) },
	} {
		state := valid
		change(&state)
		if err := ValidatePortable(state, now); err == nil {
			t.Fatal("invalid portable state accepted")
		}
	}
}

func TestGradeDistinguishesMalformedFormatFromWrongAnswer(t *testing.T) {
	malformed := strings.TrimSuffix(correctVerification(), "}")
	if got := GradeFailure(malformed, true); got != "invalid_answer_format" {
		t.Fatalf("malformed JSON classified as %q", got)
	}
	wrong := strings.Replace(correctVerification(), `14833`, `14834`, 1)
	if got := GradeFailure(wrong, true); got != "benchmark_failed" {
		t.Fatalf("wrong answer classified as %q", got)
	}
}

func fixture(t *testing.T, member string, offset bool) (*Manager, *auth.Account) {
	t.Helper()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if offset {
		if _, err := db.InsertAccount(context.Background(), "other", "other", ""); err != nil {
			t.Fatal(err)
		}
	}
	claims := `{"sub":"` + member + `","exp":9999999999}`
	token := "header." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
	id, err := db.InsertAccountWithCredentials(context.Background(), member, map[string]interface{}{"access_token": token, "account_id": "workspace", "email": member}, "")
	if err != nil {
		t.Fatal(err)
	}
	store := auth.NewStore(db, nil, &database.SystemSettings{MaxConcurrency: 4})
	t.Cleanup(store.Stop)
	account := &auth.Account{DBID: id, AccessToken: token, AccountID: "workspace", Email: member,
		CredentialGeneration: 1, PlanType: "plus", Status: auth.StatusReady, ExpiresAt: time.Now().Add(time.Hour)}
	store.AddAccount(account)
	m := New(db, store, nil, nil)
	m.ctx = context.Background()
	return m, account
}

func response(model, answer, state string) *http.Response {
	event := map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "model": model,
		"output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": answer}}}}}}
	data, _ := json.Marshal(event)
	return &http.Response{StatusCode: 200, Header: http.Header{Header: []string{state}}, Body: io.NopCloser(strings.NewReader("data: " + string(data) + "\n\n"))}
}

func runOne(t *testing.T, m *Manager) {
	t.Helper()
	row, err := m.db.ClaimStatePoolJob(context.Background(), m.owner, m.now())
	if err != nil || row == nil {
		t.Fatalf("claim: %v", err)
	}
	m.run(*row)
}

func TestCaptureExportImportAcrossDifferentLocalIDs(t *testing.T) {
	source, account := fixture(t, "same-member", false)
	calls := 0
	source.execute = func(ctx context.Context, a *auth.Account, body []byte, proxy string, headers http.Header) (*http.Response, error) {
		calls++
		if calls == 1 {
			if headers.Get(Header) != "" {
				t.Fatal("capture contaminated by existing state")
			}
			return response(Models[0], correctCandy, "original-state"), nil
		}
		if headers.Get(Header) != "original-state" || !strings.Contains(string(body), `"x-codex-turn-state":"original-state"`) {
			t.Fatal("replay did not carry original state")
		}
		return response(Models[0], correctVerification(), "replacement-must-not-win"), nil
	}
	if _, err := source.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true); err != nil {
		t.Fatal(err)
	}
	runOne(t, source)
	entries := source.Entries()
	if len(entries) != 1 || calls != 2 || entries[0].Status != "ready" {
		jobs, _ := source.Jobs(context.Background())
		t.Fatalf("entries=%v calls=%d jobs=%+v", entries, calls, jobs)
	}
	pack, err := source.Export([]string{entries[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(pack)
	if strings.Contains(string(encoded), account.AccessToken) || strings.Contains(string(encoded), "credential_hash") {
		t.Fatal("export leaked credentials")
	}
	if pack.States[0].Value != "original-state" {
		t.Fatal("response renewed original state")
	}
	target, otherID := fixture(t, "same-member", true)
	if account.ID() == otherID.ID() {
		t.Fatal("fixture IDs should differ")
	}
	otherID.Mu().Lock()
	otherID.AccessToken = "other-header." + strings.Split(otherID.AccessToken, ".")[1] + ".other-signature"
	otherID.Mu().Unlock()
	target.execute = func(ctx context.Context, a *auth.Account, body []byte, proxy string, headers http.Header) (*http.Response, error) {
		if a.ID() != otherID.ID() || headers.Get(Header) != "original-state" {
			t.Fatal("host-local remapping failed")
		}
		return response(Models[0], correctVerification(), "new-target-state"), nil
	}
	if _, err := target.Import(context.Background(), pack, true, true); err != nil {
		t.Fatal(err)
	}
	if len(target.Entries()) != 0 {
		t.Fatal("import was trusted before local validation")
	}
	runOne(t, target)
	imported := target.Entries()
	if len(imported) != 1 || imported[0].ExpiresAt != pack.States[0].ExpiresAt || imported[0].CapturedAt != pack.States[0].CapturedAt {
		t.Fatal("import lost original lifetime")
	}
	state, err := target.Resolve(otherID, Models[0], "high", "", "")
	if err != nil || state != "original-state" {
		t.Fatalf("resolve=%q, %v", state, err)
	}
	if state, _ := target.Resolve(otherID, Models[1], "high", "", ""); state != "" {
		t.Fatal("state crossed model")
	}
	if state, _ := target.Resolve(otherID, Models[0], "low", "", ""); state != "" {
		t.Fatal("state crossed effort")
	}
	if state, _ := target.Resolve(otherID, Models[0], "high", "", "native-state"); state != "" {
		t.Fatal("native state overridden")
	}
	otherID.Mu().Lock()
	otherID.ProxyURL = "http://different-proxy:9000"
	otherID.Mu().Unlock()
	if err := target.Configure(context.Background(), imported[0].ID, true, true); err == nil {
		t.Fatal("identity-changed state enabled without revalidation")
	}
	otherID.Mu().Lock()
	otherID.ProxyURL = ""
	otherID.Mu().Unlock()
	target.now = func() time.Time { return time.Unix(imported[0].ExpiresAt, 0) }
	if _, err := target.Resolve(otherID, Models[0], "high", "", ""); err == nil {
		t.Fatal("expired strict state accepted")
	}
}

func TestFailedStreamAndWrongModelNeverPublish(t *testing.T) {
	for _, kind := range []string{"overload", "wrong_model", "missing_terminal", "bad_answer"} {
		t.Run(kind, func(t *testing.T) {
			m, account := fixture(t, "member", false)
			m.execute = func(context.Context, *auth.Account, []byte, string, http.Header) (*http.Response, error) {
				switch kind {
				case "overload":
					return &http.Response{StatusCode: 200, Header: http.Header{Header: []string{"state"}}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"error\",\"code\":\"server_is_overloaded\"}\n\n"))}, nil
				case "wrong_model":
					return response(Models[1], correctCandy, "state"), nil
				case "missing_terminal":
					return &http.Response{StatusCode: 200, Header: http.Header{Header: []string{"state"}}, Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, nil
				default:
					return response(Models[0], `{"total":29}`, "state"), nil
				}
			}
			if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true); err != nil {
				t.Fatal(err)
			}
			runOne(t, m)
			if len(m.Entries()) != 0 {
				t.Fatal("failed state published")
			}
			jobs, err := m.Jobs(context.Background())
			if err != nil || jobs[0].Status != "failed" {
				t.Fatalf("jobs=%+v err=%v", jobs, err)
			}
		})
	}
}

func TestImportRejectsDifferentMember(t *testing.T) {
	m, account := fixture(t, "local-member", false)
	identity, _ := Snapshot(account, "")
	now := time.Now().Unix()
	pack := Package{Format: "codex2api-state", Version: 1, States: []PortableState{{MemberHash: Hash("other-member"), WorkspaceHash: identity.WorkspaceHash,
		Model: Models[0], Effort: "high", Value: "state", Fingerprint: Hash("state"), CapturedAt: now, ExpiresAt: now + 3600}}}
	if _, err := m.Import(context.Background(), pack, true, true); err == nil {
		t.Fatal("different member accepted")
	}
}

func TestStatePoolStreamRateLimitReportsCooldown(t *testing.T) {
	m, account := fixture(t, "member", false)
	until := time.Now().Add(15 * time.Minute).Truncate(time.Second)
	m.execute = func(context.Context, *auth.Account, []byte, string, http.Header) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("data: {\"type\":\"error\",\"code\":\"rate_limit_exceeded\"}\n\n"))}, nil
	}
	m.failure = func(a *auth.Account, _ string, _ *http.Response, _ []byte) {
		a.SetCooldownUntil(until, "rate_limit")
	}
	if _, err := m.Capture(context.Background(), []int64{account.ID()}, []string{"sol"}, true, true); err != nil {
		t.Fatal(err)
	}
	runOne(t, m)
	jobs, err := m.Jobs(context.Background())
	if err != nil || len(jobs) != 1 || jobs[0].RetryAt != until.Unix() || jobs[0].Status != "failed" {
		t.Fatalf("rate limit not reported: jobs=%+v err=%v", jobs, err)
	}
	if len(m.Entries()) != 0 {
		t.Fatal("rate-limited response published")
	}
}
