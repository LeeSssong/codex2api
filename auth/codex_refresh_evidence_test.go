package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/codex2api/database"
)

func TestCodexRefreshReloadsCapabilityEvidenceBeforePublication(t *testing.T) {
	for _, test := range []struct {
		name, workspace, capability string
		resultCount                 int
	}{
		{"same identity keeps verified capability", "workspace-1", database.CapabilitySupported, 1},
		{"changed identity invalidates verified capability", "workspace-2", database.CapabilityUnknown, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			token := func(workspace, nonce string) string {
				return makeTestJWT(map[string]any{
					"exp": time.Now().Add(time.Hour).Unix(), "nonce": nonce,
					"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": workspace, "chatgpt_plan_type": "plus"},
					"https://api.openai.com/profile": map[string]any{"email": "same-user@example.com"},
				})
			}
			newToken := token(test.workspace, "new")
			store, db, id, _ := codexRefreshFixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(map[string]any{"access_token": newToken, "refresh_token": "new-rt", "expires_in": 3600}); err != nil {
					t.Error(err)
				}
			})
			if err := db.UpdateCredentials(ctx, id, map[string]any{"access_token": token("workspace-1", "old"), "account_id": "workspace-1", "email": "same-user@example.com"}); err != nil {
				t.Fatal(err)
			}
			account := store.FindByID(id)
			if err := store.publishCodexRefresh(ctx, account); err != nil {
				t.Fatal(err)
			}
			generation := account.GetCredentialGeneration()
			now := time.Now().UTC()
			result := database.CodexProbeResult{AccountID: id, Model: "gpt-6-astra", Upstream: database.CodexPathBasispoints, Level: "basic", Outcome: "supported", Capability: database.CapabilitySupported, BasicOutcome: "supported", StartedAt: now, FinishedAt: now}
			fact := database.CodexCapability{Upstream: result.Upstream, Model: result.Model, Capability: database.CapabilitySupported, Source: "bps_strong_probe", ObservedAt: now.UnixNano()}
			if applied, err := account.SaveCodexCapabilityProbeResult(ctx, generation, result, &fact); err != nil || !applied {
				t.Fatalf("save initial evidence=%v %v", applied, err)
			}
			if snapshot := account.CodexPathSnapshot(result.Upstream, result.Model, now); snapshot.Capability != database.CapabilitySupported {
				t.Fatalf("fixture lacks verified capability: %+v", snapshot)
			}
			// Keep ordinary route-cache refresh disabled deterministically. The
			// publication path must force a reload, without waiting for any TTL.
			account.codexRoutes.mu.Lock()
			account.codexRoutes.loadedAt = now.Add(time.Hour)
			account.codexRoutes.mu.Unlock()
			if err := store.RefreshSingle(ctx, id); err != nil {
				t.Fatal(err)
			}
			if account.GetCredentialGeneration() != generation+1 || account.GetAccessToken() != newToken {
				t.Fatal("refresh did not publish the new credential generation")
			}
			if snapshot := account.CodexPathSnapshot(result.Upstream, result.Model, time.Now()); snapshot.Capability != test.capability {
				t.Fatalf("runtime capability after refresh=%+v, want %s", snapshot, test.capability)
			}
			results, err := account.GetCodexCapabilityProbeResults(ctx)
			if err != nil || len(results) != test.resultCount {
				t.Fatalf("runtime probe results after refresh=%+v %v", results, err)
			}
			result.StartedAt = now.Add(time.Minute)
			result.Outcome = "unsupported"
			fact.Capability, fact.ObservedAt = database.CapabilityUnsupported, result.StartedAt.UnixNano()
			if applied, err := account.SaveCodexCapabilityProbeResult(ctx, generation, result, &fact); err != nil || applied {
				t.Fatalf("old running probe crossed runtime refresh fence=%v %v", applied, err)
			}
		})
	}
}
