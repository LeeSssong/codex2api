package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex2api/internal/openaiidentity"
	"strings"
	"time"
)

type TokenGuardAccount struct {
	ID, Generation, Revision                                   int64
	Name, Platform, Type, Status, ErrorMessage, CooldownReason string
	FamilyID                                                   string
	Enabled, Locked                                            bool
	CooldownUntil                                              time.Time
	Credentials                                                map[string]any
}

func (a TokenGuardAccount) Credential(key string) string {
	v, _ := a.Credentials[key].(string)
	return strings.TrimSpace(v)
}
func (a TokenGuardAccount) Eligible() bool {
	mode := strings.ToLower(a.Credential("auth_mode"))
	upstream := strings.ToLower(a.Credential("upstream_type"))
	if a.ID <= 0 || a.Status == "deleted" || a.Platform != "openai" || a.Type != "oauth" || (upstream != "" && upstream != "codex") || (mode != "" && mode != "oauth") {
		return false
	}
	for _, key := range []string{"api_key", "agent_private_key", "pat", "pat_token", "personal_access_token"} {
		if a.Credential(key) != "" {
			return false
		}
	}
	// Bare access-token/PAT imports cannot establish renewable OAuth identity.
	return a.Credential("refresh_token") != "" || a.Credential("session_token") != ""
}
func (a TokenGuardAccount) Identity() (string, string) {
	email, workspace := openaiidentity.TokenIdentity(a.Credential("id_token"), a.Credential("access_token"))
	if email == "" || workspace == "" {
		email = a.Credential("email")
		workspace = openaiidentity.NormalizeWorkspaceID(a.Credential("account_id"))
	}
	return strings.ToLower(email), workspace
}
func (a TokenGuardAccount) Schedulable() bool {
	return a.Enabled && !a.Locked && a.Status == "active" && a.ErrorMessage == "" && !a.CooldownUntil.After(time.Now())
}

const guardAccountColumns = "id,name,platform,type,credentials,status,COALESCE(error_message,''),COALESCE(enabled,TRUE),COALESCE(locked,FALSE),credential_generation,control_revision,COALESCE(cooldown_reason,''),cooldown_until,COALESCE(credential_family_id,'')"

func scanGuardAccount(row interface{ Scan(...any) error }) (*TokenGuardAccount, error) {
	var a TokenGuardAccount
	var raw, until any
	err := row.Scan(&a.ID, &a.Name, &a.Platform, &a.Type, &raw, &a.Status, &a.ErrorMessage, &a.Enabled, &a.Locked, &a.Generation, &a.Revision, &a.CooldownReason, &until, &a.FamilyID)
	if err != nil {
		return nil, err
	}
	a.Credentials = decodeCredentials(raw)
	a.CooldownUntil, _ = parseDBTimeValue(until)
	return &a, nil
}
func (db *DB) TokenGuardAccount(ctx context.Context, id int64) (*TokenGuardAccount, error) {
	return scanGuardAccount(db.conn.QueryRowContext(ctx, "SELECT "+guardAccountColumns+" FROM accounts WHERE id=$1 AND status<>'deleted'", id))
}
func (db *DB) guardAccountTx(ctx context.Context, tx *sql.Tx, snapshot TokenGuardAccount) (*TokenGuardAccount, error) {
	revision, err := db.LockAccountControlTx(ctx, tx, snapshot.ID)
	if err != nil {
		return nil, err
	}
	if revision != snapshot.Revision {
		return nil, ErrTokenGuardStale
	}
	a, err := scanGuardAccount(tx.QueryRowContext(ctx, "SELECT "+guardAccountColumns+" FROM accounts WHERE id=$1", snapshot.ID))
	if err != nil {
		return nil, err
	}
	if !a.Eligible() || a.Generation != snapshot.Generation || a.Credential("refresh_token") != snapshot.Credential("refresh_token") || a.Credential("access_token") != snapshot.Credential("access_token") {
		return nil, ErrTokenGuardStale
	}
	return a, nil
}
func (db *DB) TokenGuardAccounts(ctx context.Context, groups []int64, limit int) ([]TokenGuardAccount, error) {
	query := "SELECT " + guardAccountColumns + " FROM accounts a LEFT JOIN account_token_guard_states s ON s.account_id=a.id WHERE a.status<>'deleted' AND a.platform='openai' AND a.type='oauth'"
	args := []any{}
	if len(groups) > 0 {
		ps := make([]string, len(groups))
		for i, id := range groups {
			ps[i] = fmt.Sprintf("$%d", i+1)
			args = append(args, id)
		}
		query += " AND EXISTS(SELECT 1 FROM account_group_members gm WHERE gm.account_id=a.id AND gm.group_id IN (" + strings.Join(ps, ",") + "))"
	}
	query += " ORDER BY COALESCE(s.last_probe_at,0),a.id"
	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []TokenGuardAccount{}
	for rows.Next() {
		a, err := scanGuardAccount(rows)
		if err != nil {
			return nil, err
		}
		if a.Eligible() {
			accounts = append(accounts, *a)
		}
		if limit > 0 && len(accounts) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return accounts, nil
}

// PublishTokenGuardCredentials is deliberately not the native rotating-RT finish:
// fresh external authorization must not erase the consumed-RT journal, carry
// old capability evidence, or activate an account as a side effect.
func (db *DB) PublishTokenGuardCredentials(ctx context.Context, j TokenGuardJob, snapshot TokenGuardAccount, updates map[string]any) (*TokenGuardAccount, error) {
	clean := map[string]any{}
	for _, key := range []string{"access_token", "refresh_token", "id_token"} {
		value, ok := updates[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, errors.New("重登凭据不完整")
		}
		clean[key] = value
	}
	for _, key := range []string{"expires_at", "token_type"} {
		if v, ok := updates[key]; ok {
			switch v.(type) {
			case string, float64:
				clean[key] = v
			}
		}
	}
	oldEmail, oldWorkspace := snapshot.Identity()
	newEmail, newWorkspace := openaiidentity.TokenIdentity(clean["id_token"].(string), clean["access_token"].(string))
	if oldEmail == "" || oldWorkspace == "" || !strings.EqualFold(oldEmail, newEmail) || oldWorkspace != newWorkspace {
		return nil, errors.New("重登凭据身份与目标账号不一致")
	}
	var result *TokenGuardAccount
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.guardJobTx(ctx, tx, j); err != nil {
			return err
		}
		a, err := db.guardAccountTx(ctx, tx, snapshot)
		if err != nil {
			return err
		}
		email, workspace := a.Identity()
		if !strings.EqualFold(email, oldEmail) || workspace != oldWorkspace {
			return ErrTokenGuardStale
		}
		for k, v := range clean {
			a.Credentials[k] = v
		}
		delete(a.Credentials, "codex_refresh_error")
		if a.FamilyID == "" {
			a.FamilyID = credentialFamilyCandidate(a.Credentials)
		}
		a.Credentials["credential_family_id"] = a.FamilyID
		encoded, err := json.Marshal(encryptSensitiveCredentials(a.Credentials))
		if err != nil {
			return err
		}
		value := "$1"
		if !db.isSQLite() {
			value += "::jsonb"
		}
		res, err := tx.ExecContext(ctx, "UPDATE accounts SET credentials="+value+",credential_family_id=$5,credential_generation=credential_generation+1,updated_at=CURRENT_TIMESTAMP WHERE id=$2 AND credential_generation=$3 AND control_revision=$4 AND status<>'deleted'", string(encoded), a.ID, a.Generation, a.Revision, a.FamilyID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrTokenGuardStale
		}
		for _, table := range []string{"account_codex_capabilities", "account_codex_probes"} {
			if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET credential_generation=-1 WHERE account_id=$1 AND credential_generation=0", a.ID); err != nil {
				return err
			}
		}
		if err = db.AccountControlOutboxTx(ctx, tx, a.ID); err != nil {
			return err
		}
		a.Generation++
		result = a
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (db *DB) IsolateTokenGuardAccount(ctx context.Context, j TokenGuardJob, snapshot TokenGuardAccount) (*TokenGuardAccount, bool, error) {
	var result *TokenGuardAccount
	changed := false
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.guardJobTx(ctx, tx, j); err != nil {
			return err
		}
		a, err := db.guardAccountTx(ctx, tx, snapshot)
		if err != nil {
			return err
		}
		result = a
		if !a.Enabled || a.Locked || a.Status != "active" || a.ErrorMessage != "" {
			return nil
		}
		// Save only the fields this guard changes. Existing limits are never owned.
		previousStatus, previousError := a.Status, a.ErrorMessage
		if _, err = tx.ExecContext(ctx, "UPDATE accounts SET status='error',error_message=$1,updated_at=CURRENT_TIMESTAMP WHERE id=$2 AND control_revision=$3", TokenGuardOwnedError, a.ID, a.Revision); err != nil {
			return err
		}
		revision, err := db.LockAccountControlTx(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO account_token_guard_ownership(account_id,control_revision,previous_status,previous_error,created_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id) DO UPDATE SET control_revision=EXCLUDED.control_revision,previous_status=EXCLUDED.previous_status,previous_error=EXCLUDED.previous_error,created_at=EXCLUDED.created_at", a.ID, revision, previousStatus, previousError, time.Now().Unix())
		if err != nil {
			return err
		}
		if err = db.AccountControlOutboxTx(ctx, tx, a.ID); err != nil {
			return err
		}
		a.Revision = revision
		a.Status = "error"
		a.ErrorMessage = TokenGuardOwnedError
		changed = true
		return nil
	})
	return result, changed, err
}
func (db *DB) RecoverTokenGuardAccount(ctx context.Context, j TokenGuardJob, snapshot TokenGuardAccount) (*TokenGuardAccount, bool, error) {
	var result *TokenGuardAccount
	changed := false
	err := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.guardJobTx(ctx, tx, j); err != nil {
			return err
		}
		a, err := db.guardAccountTx(ctx, tx, snapshot)
		if err != nil {
			return err
		}
		result = a
		var revision int64
		var status, message string
		err = tx.QueryRowContext(ctx, "SELECT control_revision,previous_status,previous_error FROM account_token_guard_ownership WHERE account_id=$1", a.ID).Scan(&revision, &status, &message)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if revision != a.Revision || a.Status != "error" || a.ErrorMessage != TokenGuardOwnedError || !a.Enabled || a.Locked {
			return nil
		}
		if _, err = tx.ExecContext(ctx, "UPDATE accounts SET status=$1,error_message=$2,updated_at=CURRENT_TIMESTAMP WHERE id=$3 AND control_revision=$4", status, message, a.ID, revision); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM account_token_guard_ownership WHERE account_id=$1 AND control_revision=$2", a.ID, revision); err != nil {
			return err
		}
		if err = db.AccountControlOutboxTx(ctx, tx, a.ID); err != nil {
			return err
		}
		a.Revision, err = db.LockAccountControlTx(ctx, tx, a.ID)
		a.Status = status
		a.ErrorMessage = message
		changed = err == nil
		return err
	})
	return result, changed, err
}

type TokenGuardEventRow struct {
	ID, AccountID int64
	Kind, Detail  string
	LatencyMS     int
	CreatedAt     time.Time
}

func (db *DB) RecordTokenGuardState(ctx context.Context, j TokenGuardJob, snapshot TokenGuardAccount, raw, kind, detail string, latency int) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if err := db.guardJobTx(ctx, tx, j); err != nil {
			return err
		}
		if _, err := db.guardAccountTx(ctx, tx, snapshot); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO account_token_guard_states(account_id,state,last_probe_at) VALUES($1,$2,$3) ON CONFLICT(account_id) DO UPDATE SET state=EXCLUDED.state,last_probe_at=EXCLUDED.last_probe_at", snapshot.ID, raw, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		if kind != "" {
			_, err = tx.ExecContext(ctx, "INSERT INTO account_token_guard_events(account_id,kind,detail,latency_ms,created_at) VALUES($1,$2,$3,$4,$5)", snapshot.ID, kind, detail, latency, time.Now().Unix())
		}
		return err
	})
}
func (db *DB) TokenGuardStates(ctx context.Context) ([]string, error) {
	rows, err := db.conn.QueryContext(ctx, "SELECT s.state FROM account_token_guard_states s JOIN accounts a ON a.id=s.account_id WHERE a.status<>'deleted' ORDER BY s.account_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}
func (db *DB) TokenGuardEvents(ctx context.Context, before int64, limit int) ([]TokenGuardEventRow, int64, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	query := "SELECT id,account_id,kind,detail,latency_ms,created_at FROM account_token_guard_events"
	args := []any{}
	if before > 0 {
		query += " WHERE id<$1"
		args = append(args, before)
	}
	query += fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args)+1)
	args = append(args, limit+1)
	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TokenGuardEventRow{}
	var cursor int64
	for rows.Next() {
		var e TokenGuardEventRow
		var ts int64
		if err := rows.Scan(&e.ID, &e.AccountID, &e.Kind, &e.Detail, &e.LatencyMS, &ts); err != nil {
			return nil, 0, err
		}
		e.CreatedAt = time.Unix(ts, 0).UTC()
		if len(out) == limit {
			cursor = out[len(out)-1].ID
			break
		}
		out = append(out, e)
	}
	return out, cursor, rows.Err()
}
func (db *DB) PruneTokenGuardEvents(ctx context.Context, before time.Time) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM account_token_guard_events WHERE created_at<$1", before.Unix())
		return err
	})
}
