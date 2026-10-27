package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/codex2api/credentialops"
	"net/url"
	"strings"
	"time"
)

type CredentialOpsRuntimeSettings struct {
	Enabled                        bool   `json:"enabled"`
	Engine                         string `json:"engine"`
	WorkerConcurrency              int    `json:"worker_concurrency"`
	SessionStudioEndpoint          string `json:"session_studio_endpoint"`
	SessionStudioHeaders           string `json:"-"`
	SessionStudioHeadersConfigured bool   `json:"session_studio_headers_configured"`
}

func (db *DB) CredentialOpsRuntime(ctx context.Context) (CredentialOpsRuntimeSettings, error) {
	r := CredentialOpsRuntimeSettings{Enabled: true, Engine: "local_worker", WorkerConcurrency: 3}
	var raw string
	e := db.conn.QueryRowContext(ctx, "SELECT value FROM credential_operations_settings WHERE key='runtime'").Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return r, nil
	}
	if e != nil {
		return r, e
	}
	e = json.Unmarshal([]byte(raw), &r)
	var cipher string
	err := db.conn.QueryRowContext(ctx, "SELECT value FROM credential_operations_settings WHERE key='session_studio_headers'").Scan(&cipher)
	if err != nil && err != sql.ErrNoRows {
		return r, err
	}
	r.SessionStudioHeadersConfigured = cipher != ""
	r.SessionStudioHeaders = cipher
	return r, e
}
func (db *DB) SaveCredentialOpsRuntime(ctx context.Context, r CredentialOpsRuntimeSettings) error {
	if r.Engine != "local_worker" && r.Engine != "session_studio" {
		return errors.New("invalid login engine")
	}
	if r.WorkerConcurrency < 1 || r.WorkerConcurrency > 16 {
		return errors.New("worker concurrency must be 1–16")
	}
	if r.SessionStudioEndpoint != "" {
		u, e := url.Parse(r.SessionStudioEndpoint)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("Session Studio endpoint must be HTTPS")
		}
	}
	headers := r.SessionStudioHeaders
	r.SessionStudioHeaders = ""
	raw, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, "INSERT INTO credential_operations_settings(key,value) VALUES('runtime',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw)); e != nil {
			return e
		}
		if headers != "" {
			var parsed map[string]string
			if json.Unmarshal([]byte(headers), &parsed) != nil {
				return errors.New("headers must be a JSON object of strings")
			}
			var previousCipher string
			if e = tx.QueryRowContext(ctx, "SELECT value FROM credential_operations_settings WHERE key='session_studio_headers'").Scan(&previousCipher); e != nil && e != sql.ErrNoRows {
				return e
			}
			if previousCipher != "" {
				_, dec := credentialops.EnvCrypto()
				oldRaw, e := dec(previousCipher)
				if e != nil {
					return e
				}
				var old map[string]string
				if json.Unmarshal([]byte(oldRaw), &old) != nil {
					return errors.New("saved Session Studio headers are unavailable")
				}
				for key, value := range parsed {
					if strings.TrimSpace(value) == "" {
						delete(parsed, key)
						for oldKey, oldValue := range old {
							if strings.EqualFold(oldKey, key) {
								parsed[key] = oldValue
								break
							}
						}
					}
				}
			}
			for key, value := range parsed {
				lower := strings.ToLower(strings.TrimSpace(key))
				if lower == "host" || lower == "content-length" || lower == "transfer-encoding" || lower == "connection" || lower == "x-codex2api-credential-worker" || strings.ContainsAny(key+value, "\r\n") {
					return errors.New("reserved or unsafe Session Studio header")
				}
			}
			enc, _ := credentialops.EnvCrypto()
			normalized, e := json.Marshal(parsed)
			if e != nil {
				return e
			}
			cipher, e := enc(string(normalized))
			if e != nil {
				return e
			}
			if _, e = tx.ExecContext(ctx, "INSERT INTO credential_operations_settings(key,value) VALUES('session_studio_headers',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value", cipher); e != nil {
				return e
			}
		}
		return tx.Commit()
	})
}
func (db *DB) credentialOperationsEnabledTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var raw string
	e := tx.QueryRowContext(ctx, "SELECT value FROM credential_operations_settings WHERE key='runtime'").Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return true, nil
	}
	if e != nil {
		return false, e
	}
	var r CredentialOpsRuntimeSettings
	r.Enabled = true
	e = json.Unmarshal([]byte(raw), &r)
	return r.Enabled, e
}
func (db *DB) HasCredentialOperationsCiphertext(ctx context.Context) (bool, error) {
	var n int
	e := db.conn.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM credential_operations_login_configs)+(SELECT COUNT(*) FROM credential_operations_task_inputs)+(SELECT COUNT(*) FROM credential_operations_profile_grants)+(SELECT COUNT(*) FROM credential_operations_totp_recoveries)+(SELECT COUNT(*) FROM credential_operations_settings WHERE key='session_studio_headers' AND value<>'')").Scan(&n)
	return n > 0, e
}
func (db *DB) DeleteCredentialOpsMonitor(ctx context.Context, id int64) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, "DELETE FROM credential_operations_monitors WHERE account_id=$1", id); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET status='cancelled',stage='cancelled',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP WHERE account_id=$1 AND status IN ('queued','running')", id); e != nil {
			return e
		}
		return tx.Commit()
	})
}

type CredentialOpsProfileGrant struct {
	AccountID      int64
	Profile        string
	Generation     int64
	Credentials    map[string]any
	RefreshPending bool
	Invalidated    bool
	UpdatedAt      time.Time
}

func (db *DB) GetCredentialOpsProfileGrant(ctx context.Context, id int64, profile string) (*CredentialOpsProfileGrant, error) {
	r := &CredentialOpsProfileGrant{AccountID: id, Profile: profile}
	var cipher string
	e := db.conn.QueryRowContext(ctx, "SELECT generation,credentials_ciphertext,invalidated,updated_at,refresh_pending FROM credential_operations_profile_grants WHERE account_id=$1 AND profile=$2", id, profile).Scan(&r.Generation, &cipher, &r.Invalidated, &r.UpdatedAt, &r.RefreshPending)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	_, dec := credentialops.EnvCrypto()
	raw, e := dec(cipher)
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal([]byte(raw), &r.Credentials)
	return r, e
}
func (db *DB) UpdateCredentialOpsProfileGrantCAS(ctx context.Context, id int64, profile string, generation int64, credentials map[string]any) (bool, error) {
	raw, e := json.Marshal(credentials)
	if e != nil {
		return false, e
	}
	enc, _ := credentialops.EnvCrypto()
	cipher, e := enc(string(raw))
	if e != nil {
		return false, e
	}
	res, e := db.conn.ExecContext(ctx, "UPDATE credential_operations_profile_grants SET credentials_ciphertext=$1,generation=generation+1,invalidated=FALSE,refresh_pending=FALSE,updated_at=CURRENT_TIMESTAMP WHERE account_id=$2 AND profile=$3 AND generation=$4", cipher, id, profile, generation)
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	return n == 1, e
}
func (db *DB) InvalidateCredentialOpsProfileGrantCAS(ctx context.Context, id int64, profile string, generation int64) (bool, error) {
	res, e := db.conn.ExecContext(ctx, "UPDATE credential_operations_profile_grants SET invalidated=TRUE,updated_at=CURRENT_TIMESTAMP WHERE account_id=$1 AND profile=$2 AND generation=$3", id, profile, generation)
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	return n == 1, e
}

type CredentialOpsRetryRoute struct {
	ProxyURL       string
	ConfigRevision int64
}

func (db *DB) RetryCredentialOpsTask(ctx context.Context, id int64, resolvedRoute ...CredentialOpsRetryRoute) (*CredentialOpsTaskRow, error) {
	outcome, e := db.FindCredentialOpsRetryOutcome(ctx, id)
	if e != nil || outcome != nil {
		return outcome, e
	}
	task, e := db.GetCredentialOpsTask(ctx, id)
	if e != nil || task == nil {
		return task, e
	}
	if task.Kind != "login" {
		return nil, errors.New("only login tasks can be retried; use recovery verification for 2FA")
	}
	if task.Status == "queued" || task.Status == "running" || task.Status == "succeeded" {
		return task, nil
	}
	cfg, e := db.CredentialOpsTaskConfig(ctx, id)
	if e != nil {
		return nil, e
	}

	prior := cfg
	retrySource := prior.RetrySourceID
	if retrySource == 0 {
		retrySource = task.ID
	}
	if task.AccountID > 0 {
		row, e := db.GetAccountByID(ctx, task.AccountID)
		if e != nil || row == nil {
			return nil, ErrCredentialOpsStale
		}
		task.ExpectedGeneration = row.CredentialGeneration
		current, e := db.GetCredentialOpsLoginConfig(ctx, task.AccountID)
		if e != nil || current == nil {
			return nil, ErrCredentialOpsStale
		}
		if len(resolvedRoute) > 0 && resolvedRoute[0].ConfigRevision != current.Revision {
			return nil, ErrCredentialOpsStale
		}
		cfg = current
	}
	cfg.RetrySourceID = retrySource
	cfg.OAuthProfile = task.OAuthProfile
	if cfg.OAuthProfile == "excel" {
		cfg.Engine = "local_worker"
	}
	if len(resolvedRoute) > 0 {
		if resolvedRoute[0].ConfigRevision != cfg.Revision {
			return nil, ErrCredentialOpsStale
		}
		cfg.ResolvedProxyURL = resolvedRoute[0].ProxyURL
		cfg.RouteResolved = true
	} else if task.AccountID == 0 && prior.ProxyURLCiphertext != "" {
		_, dec := credentialops.EnvCrypto()
		cfg.ResolvedProxyURL, e = dec(prior.ProxyURLCiphertext)
		if e != nil {
			return nil, e
		}
		cfg.RouteResolved = true
	} else if cfg.ProxySource == "account" || cfg.ProxySource == "global" {
		return nil, errors.New("retry requires resolved account egress")
	}
	cfg.ImportKey = ""
	return db.CreateCredentialOpsTaskWithConfig(ctx, task.AccountID, task.ExpectedGeneration, *cfg)
}

func (db *DB) ListCredentialOpsMissingExcelAccounts(ctx context.Context, globalEnabled bool) ([]int64, error) {
	rows, e := db.conn.QueryContext(ctx, "SELECT c.account_id FROM credential_operations_login_configs c JOIN accounts a ON a.id=c.account_id LEFT JOIN credential_operations_profile_grants g ON g.account_id=c.account_id AND g.profile='excel' LEFT JOIN account_codex_paths p ON p.account_id=a.id AND p.upstream='basispoints' WHERE a.platform='openai' AND a.type='oauth' AND a.status<>'deleted' AND a.enabled=TRUE AND c.credential_mode='password_totp' AND c.password_ciphertext<>'' AND "+db.credentialOpsExcelEligibilitySQL("a")+" AND (g.account_id IS NULL OR g.invalidated=TRUE OR (g.refresh_pending=TRUE AND g.updated_at<$2)) AND (($1=TRUE AND COALESCE(p.allowed,1)<>0) OR p.allowed=1) ORDER BY c.account_id LIMIT 100", globalEnabled, db.timeArg(time.Now().UTC().Add(-5*time.Minute)))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (db *DB) ReconcileCredentialOpsExpiredTOTP(ctx context.Context) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_tasks SET status='failed',stage='uncertain',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP WHERE kind LIKE 'totp_%' AND status='running' AND lease_until<CURRENT_TIMESTAMP"); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE credential_operations_totp_recoveries SET state='uncertain',updated_at=CURRENT_TIMESTAMP WHERE task_id IN (SELECT id FROM credential_operations_tasks WHERE kind LIKE 'totp_%' AND status IN ('failed','cancelled')) AND state NOT IN ('adopted','old_verified')"); e != nil {
			return e
		}
		return tx.Commit()
	})
}

func CredentialOperationsEligible(row *AccountRow) bool {
	if row == nil || row.Platform != "openai" || row.Type != "oauth" || row.Status == "deleted" {
		return false
	}
	if row.GetCredential("client_id") == credentialops.ExcelClientID {
		return false
	}
	if !workspaceIdentityMigrationEligible(row.Credentials) {
		return false
	}
	mode := strings.ToLower(row.GetCredential("auth_mode"))
	return mode != "pat" && mode != "api_key"
}

func (db *DB) BeginCredentialOpsProfileRefresh(ctx context.Context, id int64, profile string, generation int64) (bool, error) {
	applied := false
	err := db.withCredentialOpsDurableTx(ctx, func(tx *sql.Tx) error {
		result, e := tx.ExecContext(ctx, "UPDATE credential_operations_profile_grants SET refresh_pending=TRUE,updated_at=CURRENT_TIMESTAMP WHERE account_id=$1 AND profile=$2 AND generation=$3 AND invalidated=FALSE AND refresh_pending=FALSE", id, profile, generation)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		applied = n == 1
		return nil
	})
	return applied && err == nil, err
}
