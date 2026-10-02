package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/codex2api/plugins"
)

func TestCredentialOpsMonitorConfigurationFence(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			dsn := ":memory:"
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
			if err = db.EnsureCredentialOpsSchema(ctx); err != nil {
				t.Fatal(err)
			}
			if err = NewPluginStore(db).Put(ctx, plugins.Setting{ID: "credential-ops", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			fixture := func() (CredentialOpsMonitorRow, CredentialOpsLoginConfigRow, int64) {
				email := fmt.Sprintf("monitor-fence-%d@example.com", time.Now().UnixNano())
				id, e := db.InsertAccountWithCredentials(ctx, email, map[string]any{"email": email, "workspace_id": "workspace-monitor-fence", "access_token": "fixture-only"}, "")
				if e != nil {
					t.Fatal(e)
				}
				row, e := db.GetAccountByID(ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				m := CredentialOpsMonitorRow{AccountID: id, Enabled: true, AutoRelogin: true, IntervalSeconds: 60, FailureThreshold: 1, CooldownSeconds: 60}
				if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
					t.Fatal(e)
				}
				return m, CredentialOpsLoginConfigRow{AccountID: id, LoginEmail: email, CredentialMode: "password_totp", Engine: "local_worker", PasswordCiphertext: "enc:v1:fixture"}, row.CredentialGeneration
			}
			for _, flag := range []string{"monitor", "auto_relogin"} {
				t.Run(flag, func(t *testing.T) {
					m, _, _ := fixture()
					claimed, e := db.ClaimCredentialOpsMonitor(ctx, m.AccountID, "same-owner")
					if e != nil || claimed == nil {
						t.Fatalf("claim %#v %v", claimed, e)
					}
					if flag == "monitor" {
						m.Enabled = false
					} else {
						m.AutoRelogin = false
					}
					if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
						t.Fatal(e)
					}
					if reauth, e := db.CompleteCredentialOpsMonitor(ctx, *claimed, "auth"); reauth || !errors.Is(e, ErrCredentialOpsStale) {
						t.Fatalf("stale probe reauth=%v err=%v", reauth, e)
					}
					fresh, e := db.ClaimCredentialOpsMonitor(ctx, m.AccountID, "same-owner")
					if e != nil || fresh == nil {
						t.Fatalf("fresh claim %#v %v", fresh, e)
					}
					if fresh.LeaseOwner == claimed.LeaseOwner {
						t.Fatal("same caller reused an old lease identity")
					}
					if reauth, e := db.CompleteCredentialOpsMonitor(ctx, *claimed, "auth"); reauth || !errors.Is(e, ErrCredentialOpsStale) {
						t.Fatal("old callback consumed fresh probe")
					}
					if reauth, e := db.CompleteCredentialOpsMonitor(ctx, *fresh, "auth"); e != nil || reauth {
						t.Fatalf("disabled flag spawned login %v %v", reauth, e)
					}
					var count int
					if e = db.conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM credential_ops_tasks WHERE account_id=$1`, m.AccountID).Scan(&count); e != nil || count != 0 {
						t.Fatalf("disabled probe queued %d tasks: %v", count, e)
					}
				})
			}
			t.Run("completed_probe_save_and_selective_cancel", func(t *testing.T) {
				m, cfg, generation := fixture()
				claimed, e := db.ClaimCredentialOpsMonitor(ctx, m.AccountID, "probe-owner")
				if e != nil || claimed == nil {
					t.Fatal(e)
				}
				if reauth, e := db.CompleteCredentialOpsMonitor(ctx, *claimed, "auth"); e != nil || !reauth {
					t.Fatalf("auth %v %v", reauth, e)
				}
				m.AutoRelogin = false
				if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
					t.Fatal(e)
				}
				if _, e = db.CreateCredentialOpsAutoTaskWithConfig(ctx, m.AccountID, generation, cfg, claimed.LeaseOwner); !errors.Is(e, ErrCredentialOpsStale) {
					t.Fatalf("completion/save/queue gap %v", e)
				}
				m.AutoRelogin = true
				if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
					t.Fatal(e)
				}
				if _, e = db.CreateCredentialOpsAutoTaskWithConfig(ctx, m.AccountID, generation, cfg, claimed.LeaseOwner); !errors.Is(e, ErrCredentialOpsStale) {
					t.Fatalf("disable/re-enable reused completion %v", e)
				}
				fresh, e := db.ClaimCredentialOpsMonitor(ctx, m.AccountID, "probe-owner")
				if e != nil || fresh == nil {
					t.Fatal(e)
				}
				if reauth, e := db.CompleteCredentialOpsMonitor(ctx, *fresh, "auth"); e != nil || !reauth {
					t.Fatalf("fresh auth %v %v", reauth, e)
				}
				automatic, e := db.CreateCredentialOpsAutoTaskWithConfig(ctx, m.AccountID, generation, cfg, fresh.LeaseOwner)
				if e != nil {
					t.Fatal(e)
				}
				snapshot, e := db.CredentialOpsTaskConfig(ctx, automatic.ID)
				if e != nil || !snapshot.Automatic {
					t.Fatal("automatic origin not durably recorded")
				}
				running, e := db.ClaimCredentialOpsTask(ctx, "automatic-worker", time.Minute)
				if e != nil || running == nil || running.ID != automatic.ID {
					t.Fatalf("automatic claim %#v %v", running, e)
				}
				m.AutoRelogin = false
				if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
					t.Fatal(e)
				}
				task, e := db.GetCredentialOpsTask(ctx, automatic.ID)
				if e != nil || task.Status != "cancelled" {
					t.Fatalf("automatic task remained active %#v %v", task, e)
				}
				manual, e := db.CreateCredentialOpsTaskWithConfig(ctx, m.AccountID, generation, cfg)
				if e != nil {
					t.Fatal(e)
				}
				m.Enabled = false
				if e = db.SaveCredentialOpsMonitor(ctx, m); e != nil {
					t.Fatal(e)
				}
				task, e = db.GetCredentialOpsTask(ctx, manual.ID)
				if e != nil || task.Status != "queued" {
					t.Fatalf("monitor edit cancelled manual task %#v %v", task, e)
				}
				_ = db.CancelCredentialOpsTask(ctx, manual.ID)
			})
			t.Run("completion_uses_current_threshold", func(t *testing.T) {
				m, _, _ := fixture()
				m.FailureThreshold = 10
				if e := db.SaveCredentialOpsMonitor(ctx, m); e != nil {
					t.Fatal(e)
				}
				claimed, e := db.ClaimCredentialOpsMonitor(ctx, m.AccountID, "rule-owner")
				if e != nil || claimed == nil {
					t.Fatal(e)
				}
				claimed.FailureThreshold = 1
				claimed.FailStreak = 20
				if reauth, e := db.CompleteCredentialOpsMonitor(ctx, *claimed, "auth"); e != nil || reauth {
					t.Fatalf("caller snapshot overrode current rules %v %v", reauth, e)
				}
			})
		})
	}
}
