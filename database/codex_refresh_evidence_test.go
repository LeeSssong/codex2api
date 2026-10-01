package database

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func codexRefreshEvidenceJWT(email, workspace, nonce string) string {
	raw, _ := json.Marshal(map[string]any{
		"https://api.openai.com/auth":    map[string]any{"chatgpt_account_id": workspace},
		"https://api.openai.com/profile": map[string]any{"email": email}, "nonce": nonce,
	})
	return "e30." + base64.RawURLEncoding.EncodeToString(raw) + ".signature"
}

func newCodexRefreshEvidenceAccount(t *testing.T, db *DB) (int64, string) {
	t.Helper()
	rt := "evidence-" + uuid.NewString()
	id, err := db.InsertAccountWithCredentials(context.Background(), "refresh-evidence", map[string]any{
		"access_token":  codexRefreshEvidenceJWT("user@example.com", "workspace-1", "old"),
		"refresh_token": rt, "email": "user@example.com", "account_id": "workspace-1",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return id, rt
}

func seedCodexRefreshEvidence(t *testing.T, db *DB, id, generation int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, level := range []string{"basic", "tools"} {
		result := CodexProbeResult{AccountID: id, Model: "gpt-6-astra", Upstream: CodexPathBasispoints, Level: level, Outcome: "supported", Capability: CapabilitySupported, BasicOutcome: "supported", ToolsOutcome: "supported", StartedAt: now, FinishedAt: now.Add(time.Second), HTTPStatus: 200, Attempts: 3}
		fact := CodexCapability{Upstream: result.Upstream, Model: result.Model, Capability: CapabilitySupported, Source: "bps_strong_probe", ObservedAt: now.UnixNano(), CredentialGeneration: generation}
		if applied, err := db.SaveCodexProbeResult(ctx, generation, result, &fact); err != nil || !applied {
			t.Fatalf("seed probe: %v %v", applied, err)
		}
	}
	if applied, err := db.ObserveCodexCapability(ctx, id, CodexCapability{Upstream: CodexPathNative, Model: "gpt-6-astra", Capability: CapabilitySupported, Source: "production", ObservedAt: now.UnixNano(), CredentialGeneration: generation}); err != nil || !applied {
		t.Fatalf("seed native evidence: %v %v", applied, err)
	}
}

func TestCodexRefreshKeepsIdentity(t *testing.T) {
	before := map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "old"), "account_id": "workspace-1", "email": "user@example.com"}
	for _, test := range []struct {
		name    string
		updates map[string]any
		want    bool
	}{
		{"same JWT identity", map[string]any{"access_token": codexRefreshEvidenceJWT("USER@example.com", "workspace-1", "new")}, true},
		{"returned account identity", map[string]any{"access_token": "opaque-token", "email": "user@example.com", "account_id": "workspace-1"}, true},
		{"missing returned identity", map[string]any{"access_token": "opaque-token"}, false},
		{"changed workspace", map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-2", "new")}, false},
		{"different member of same workspace", map[string]any{"access_token": codexRefreshEvidenceJWT("other@example.com", "workspace-1", "new")}, false},
		{"explicit route change", map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "new"), "account_id": "workspace-2"}, false},
		{"session fallback warning", map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "new"), "codex_refresh_error": "session fallback"}, false},
		{"missing access token", map[string]any{"email": "user@example.com", "account_id": "workspace-1"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := codexRefreshKeepsIdentity(before, test.updates); got != test.want {
				t.Fatalf("same identity=%v want=%v", got, test.want)
			}
		})
	}
	if codexRefreshKeepsIdentity(map[string]any{"access_token": "opaque-old"}, map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "new")}) {
		t.Fatal("missing previous identity accepted")
	}
}

func TestCodexRefreshPreservesEvidenceAndProbeFence(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "refresh-evidence.db")
			if driver == "postgres" {
				dsn = os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("requires isolated CODEX2API_TEST_POSTGRES_DSN")
				}
			}
			db, err := New(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			id, rt := newCodexRefreshEvidenceAccount(t, db)
			seedCodexRefreshEvidence(t, db, id, 1)
			if err := db.SetCodexPathAllowed(ctx, id, CodexPathNative, false); err != nil {
				t.Fatal(err)
			}
			before, err := db.GetCodexCapabilityProbeResults(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			for generation := int64(1); generation <= 2; generation++ {
				attempt, err := db.BeginCodexRefresh(ctx, id, generation, rt)
				if err != nil {
					t.Fatal(err)
				}
				nextRT := "rotated-" + rt
				updates := map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", nextRT), "refresh_token": nextRT}
				for range 2 {
					ids, err := db.FinishCodexRefresh(ctx, attempt, updates)
					if err != nil || len(ids) != 1 || ids[0] != id {
						t.Fatalf("refresh publication %v %v", ids, err)
					}
				}
				row, err := db.GetAccountByID(ctx, id)
				if err != nil || row.CredentialGeneration != generation+1 {
					t.Fatalf("rotation generation: %+v %v", row, err)
				}
				paths, facts, err := db.GetCodexRoutes(ctx, id)
				if err != nil || len(facts) != 2 || len(paths) != 1 || paths[0].Allowed {
					t.Fatalf("rotation changed route permissions/evidence: %+v %+v %v", paths, facts, err)
				}
				for _, fact := range facts {
					if fact.CredentialGeneration != generation+1 || fact.Capability != CapabilitySupported {
						t.Fatalf("lost supported capability: %+v", fact)
					}
				}
				after, err := db.GetCodexCapabilityProbeResults(ctx, id)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("probe diagnostics changed: %+v %v", after, err)
				}
				late := before[0]
				late.Outcome, late.Capability, late.StartedAt = "unsupported", CapabilityUnsupported, time.Now().Add(time.Hour)
				stale := CodexCapability{Upstream: CodexPathBasispoints, Model: late.Model, Capability: CapabilityUnsupported, Source: "bps_strong_probe", ObservedAt: late.StartedAt.UnixNano(), CredentialGeneration: generation}
				if applied, err := db.SaveCodexProbeResult(ctx, generation, late, &stale); err != nil || applied {
					t.Fatalf("old running probe passed CAS: %v %v", applied, err)
				}
				if applied, err := db.ObserveCodexCapability(ctx, id, stale); err != nil || applied {
					t.Fatalf("old production observation passed CAS: %v %v", applied, err)
				}
				rt = nextRT
			}
			t.Run("replacement never resurrects evidence", func(t *testing.T) {
				if err := db.UpdateCredentials(ctx, id, map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "admin"), "refresh_token": "admin-" + rt}); err != nil {
					t.Fatal(err)
				}
				assertCodexRefreshEvidenceHidden(t, db, id)
				row, err := db.GetAccountByID(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				attempt, err := db.BeginCodexRefresh(ctx, id, row.CredentialGeneration, "admin-"+rt)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.FinishCodexRefresh(ctx, attempt, map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "after-admin"), "refresh_token": "later-" + rt}); err != nil {
					t.Fatal(err)
				}
				assertCodexRefreshEvidenceHidden(t, db, id)
			})
			t.Run("changed identity during refresh", func(t *testing.T) {
				id, rt := newCodexRefreshEvidenceAccount(t, db)
				seedCodexRefreshEvidence(t, db, id, 1)
				attempt, err := db.BeginCodexRefresh(ctx, id, 1, rt)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.FinishCodexRefresh(ctx, attempt, map[string]any{"access_token": codexRefreshEvidenceJWT("other@example.com", "workspace-1", "changed"), "refresh_token": "changed-" + rt}); err != nil {
					t.Fatal(err)
				}
				assertCodexRefreshEvidenceHidden(t, db, id)
			})
			t.Run("administrative replacement during refresh", func(t *testing.T) {
				id, rt := newCodexRefreshEvidenceAccount(t, db)
				seedCodexRefreshEvidence(t, db, id, 1)
				attempt, err := db.BeginCodexRefresh(ctx, id, 1, rt)
				if err != nil {
					t.Fatal(err)
				}
				if err := db.UpdateCredentials(ctx, id, map[string]any{"access_token": codexRefreshEvidenceJWT("other@example.com", "workspace-1", "admin")}); err != nil {
					t.Fatal(err)
				}
				ids, err := db.FinishCodexRefresh(ctx, attempt, map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "old-result"), "refresh_token": "new-" + rt})
				if err != nil || len(ids) != 0 {
					t.Fatalf("administrative replacement overwritten: %v %v", ids, err)
				}
				assertCodexRefreshEvidenceHidden(t, db, id)
			})
		})
	}
}

func assertCodexRefreshEvidenceHidden(t *testing.T, db *DB, id int64) {
	t.Helper()
	_, facts, err := db.GetCodexRoutes(context.Background(), id)
	if err != nil || len(facts) != 0 {
		t.Fatalf("invalidated capabilities resurrected: %+v %v", facts, err)
	}
	results, err := db.GetCodexCapabilityProbeResults(context.Background(), id)
	if err != nil || len(results) != 0 {
		t.Fatalf("invalidated probe results resurrected: %+v %v", results, err)
	}
}

func TestCodexRefreshEvidenceMigrationRollsBackAtomically(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "refresh-evidence-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	id, rt := newCodexRefreshEvidenceAccount(t, db)
	seedCodexRefreshEvidence(t, db, id, 1)
	attempt, err := db.BeginCodexRefresh(ctx, id, 1, rt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `CREATE TRIGGER reject_evidence_rotation BEFORE UPDATE OF credential_generation ON account_codex_probes BEGIN SELECT RAISE(ABORT, 'injected evidence failure'); END`); err != nil {
		t.Fatal(err)
	}
	updates := map[string]any{"access_token": codexRefreshEvidenceJWT("user@example.com", "workspace-1", "new"), "refresh_token": "new-" + rt}
	if _, err := db.FinishCodexRefresh(ctx, attempt, updates); err == nil {
		t.Fatal("evidence failure did not fail refresh transaction")
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil || row.CredentialGeneration != 1 || row.GetCredential("refresh_token") != rt {
		t.Fatalf("failed migration partially rotated credentials: %+v %v", row, err)
	}
	_, facts, err := db.GetCodexRoutes(ctx, id)
	if err != nil || len(facts) != 2 {
		t.Fatalf("failed transaction lost facts: %+v %v", facts, err)
	}
	for _, fact := range facts {
		if fact.CredentialGeneration != 1 {
			t.Fatal("failed transaction partially migrated evidence")
		}
	}
	if _, err := db.conn.ExecContext(ctx, `DROP TRIGGER reject_evidence_rotation`); err != nil {
		t.Fatal(err)
	}
	if ids, err := db.FinishCodexRefresh(ctx, attempt, updates); err != nil || len(ids) != 1 {
		t.Fatalf("retry after rolled-back migration: %v %v", ids, err)
	}
}
