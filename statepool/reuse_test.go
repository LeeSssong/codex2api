package statepool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
)

func TestCredentialUpdateRevalidatesVerifiedStateBeforeCapture(t *testing.T) {
	for _, outcome := range []string{"pass", "unauthorized", "rejected"} {
		t.Run(outcome, func(t *testing.T) {
			m, account := fixture(t, "member", false)
			now := time.Now().Truncate(time.Second)
			m.now = func() time.Time { return now }
			job, err := m.accountJob(account.ID(), Models[0], "batch", true, true)
			if err != nil {
				t.Fatal(err)
			}
			original := Entry{ID: "original", AccountID: account.ID(), Model: Models[0], Effort: "high", Identity: job.Identity,
				Value: "saved", Fingerprint: Hash("saved"), CapturedAt: now.Add(-20 * time.Minute).Unix(), ExpiresAt: now.Add(40 * time.Minute).Unix(), Enabled: true}
			m.entries[key(account.ID(), Models[0], "high")] = original
			account.AccessToken += "-new"
			account.CredentialGeneration++
			calls := 0
			m.execute = func(_ context.Context, _ *auth.Account, _ []byte, _ string, headers http.Header) (*http.Response, error) {
				calls++
				if calls == 1 {
					if headers.Get(Header) != original.Value {
						t.Error("credential update collected instead of replaying")
					}
					switch outcome {
					case "unauthorized":
						return &http.Response{StatusCode: 401, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"token_revoked"}}`))}, nil
					case "rejected":
						return response(Models[0], strings.ReplaceAll(correctVerification(), "14833", "14834"), "ignored"), nil
					}
					return response(Models[0], correctVerification(), "ignored"), nil
				}
				if calls == 2 {
					if headers.Get(Header) != "" {
						t.Error("replacement capture retained the rejected state")
					}
					return response(Models[0], correctCandy, "replacement"), nil
				}
				return response(Models[0], correctVerification(), "ignored"), nil
			}
			ids, err := m.Capture(context.Background(), []int64{account.ID()}, []string{Models[0]}, true, true, CaptureOptions{Candidates: 4})
			if err != nil || len(ids) != 1 {
				t.Fatal("replay should create exactly one job", err)
			}
			row, err := m.db.ClaimStatePoolJob(context.Background(), m.owner, now)
			if err != nil || row == nil {
				t.Fatal("missing replay job", err)
			}
			var replay Job
			if err := json.Unmarshal([]byte(row.Data), &replay); err != nil {
				t.Fatal(err)
			}
			entry, err := m.validate(context.Background(), &replay, row.Secret, func() error { return nil })
			switch outcome {
			case "pass":
				if err != nil || calls != 1 || entry.Value != original.Value || entry.ExpiresAt != original.ExpiresAt || entry.CapturedAt != original.CapturedAt || entry.Identity == original.Identity {
					t.Fatal("successful replay replaced or renewed the state", err)
				}
			case "unauthorized":
				if err == nil || calls != 1 || m.entries[key(account.ID(), Models[0], "high")].Value != original.Value {
					t.Fatal("401 discarded the state or started collection")
				}
			case "rejected":
				if err != nil || calls != 3 || entry.Value != "replacement" {
					t.Fatal("explicit failed verification did not replace the state", err)
				}
			}
		})
	}
}
