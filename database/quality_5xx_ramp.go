package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/codex2api/smartops"
)

func qualityPayload(raw string, state smartops.Quality5xxRampState) []byte {
	m := map[string]json.RawMessage{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	b, _ := json.Marshal(state)
	m["quality_5xx"] = b
	out, _ := json.Marshal(m)
	return out
}

// RecordQuality5xxFailure atomically fences a ramp attempt and persists the
// state in the existing smart_ops_concurrency payload. It returns the
// concurrency to apply and whether a probe should be queued.
func (db *DB) RecordQuality5xxFailure(ctx context.Context, id int64, c smartops.OAuthAutoConfig, generation int64, revision string, models []string) (int, bool, error) {
	if !c.Quality5xx.Enabled || id <= 0 {
		return 0, false, nil
	}
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return 0, false, err
	}
	var next int
	shouldProbe := false
	err := db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var current sql.NullInt64
		var raw string
		q := `SELECT base_concurrency_override,credentials FROM accounts WHERE id=$1 AND status<>'deleted'`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		if err = tx.QueryRowContext(ctx, q, id).Scan(&current, &raw); err != nil {
			return err
		}
		if !current.Valid {
			current = sql.NullInt64{Int64: int64(c.Concurrency), Valid: true}
		}
		var state smartops.Quality5xxRampState
		if err = tx.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&raw); err != nil && err != sql.ErrNoRows {
			return err
		}
		if raw != "" {
			var envelope struct {
				Quality *smartops.Quality5xxRampState `json:"quality_5xx"`
			}
			if e := json.Unmarshal([]byte(raw), &envelope); e == nil && envelope.Quality != nil {
				state = *envelope.Quality
			} else {
				_ = json.Unmarshal([]byte(raw), &state)
			}
		}
		state = state.ResetOnFailure(c.Quality5xx, int(current.Int64), generation, revision, time.Now())
		state.OwnedModels = append([]string(nil), models...)
		next = state.CurrentConcurrency
		shouldProbe = true
		b := qualityPayload(raw, state)
		if _, err = tx.ExecContext(ctx, `INSERT INTO smart_ops_concurrency(account_id,payload) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET payload=EXCLUDED.payload`, id, string(b)); err != nil {
			return err
		}
		if next != int(current.Int64) {
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET base_concurrency_override=$1,updated_at=CURRENT_TIMESTAMP WHERE id=$2`, next, id); err != nil {
				return err
			}
			if err = insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "upsert"); err != nil {
				return err
			}
		}
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model != "" {
				if err = db.setModelCooldownTx(ctx, tx, id, model, "quality_5xx_ramp", time.Now().Add(time.Duration(c.Quality5xx.CooldownSeconds)*time.Second)); err != nil {
					return err
				}
			}
		}
		return tx.Commit()
	})
	return next, shouldProbe, err
}

func (db *DB) setModelCooldownTx(ctx context.Context, tx *sql.Tx, id int64, model, reason string, until time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO account_model_cooldowns(account_id,model,reason,reset_at,updated_at) VALUES($1,$2,$3,$4,CURRENT_TIMESTAMP) ON CONFLICT(account_id,model) DO UPDATE SET reason=excluded.reason,reset_at=excluded.reset_at,updated_at=CURRENT_TIMESTAMP`, id, model, reason, db.timeArg(until))
	return err
}

// ApplyQuality5xxProbe records a fenced probe result. Inconclusive and failed
// probes deliberately retain the ramp and cooldowns.
func (db *DB) ApplyQuality5xxProbe(ctx context.Context, id int64, c smartops.OAuthAutoConfig, generation int64, revision string, passed, conclusive bool, episode ...uint64) (int, bool, error) {
	if err := db.EnsureSmartOpsSchema(ctx); err != nil {
		return 0, false, err
	}
	var next int
	var active bool
	err := db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var current sql.NullInt64
		var raw string
		q := `SELECT base_concurrency_override,credentials FROM accounts WHERE id=$1 AND status<>'deleted'`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		if err = tx.QueryRowContext(ctx, q, id).Scan(&current, &raw); err != nil {
			return err
		}
		if !current.Valid {
			current = sql.NullInt64{Int64: int64(c.Concurrency), Valid: true}
		}
		if err = tx.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&raw); err != nil {
			return err
		}
		var s smartops.Quality5xxRampState
		var envelope struct {
			Quality *smartops.Quality5xxRampState `json:"quality_5xx"`
		}
		if err = json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Quality != nil {
			s = *envelope.Quality
		} else if err = json.Unmarshal([]byte(raw), &s); err != nil {
			return err
		}
		if len(episode) > 0 && s.Attempt != episode[0] {
			return nil
		}
		s, next = s.ProbeResult(c, int(current.Int64), generation, revision, passed, conclusive, time.Now())
		active = s.Active
		if next != int(current.Int64) {
			if _, err = tx.ExecContext(ctx, `UPDATE accounts SET base_concurrency_override=$1,updated_at=CURRENT_TIMESTAMP WHERE id=$2`, next, id); err != nil {
				return err
			}
			if err = insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "upsert"); err != nil {
				return err
			}
		}
		if passed && conclusive {
			for _, m := range s.OwnedModels {
				if _, err = tx.ExecContext(ctx, `DELETE FROM account_model_cooldowns WHERE account_id=$1 AND model=$2 AND reason='quality_5xx_ramp'`, id, m); err != nil {
					return err
				}
			}
			s.OwnedModels = nil
		}
		b := qualityPayload(raw, s)
		if _, err = tx.ExecContext(ctx, `UPDATE smart_ops_concurrency SET payload=$1 WHERE account_id=$2`, string(b), id); err != nil {
			return err
		}
		return tx.Commit()
	})
	return next, active, err
}

func (db *DB) Quality5xxAttempt(ctx context.Context, id int64) uint64 {
	var raw string
	if db.conn.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&raw) != nil {
		return 0
	}
	var e struct {
		Quality smartops.Quality5xxRampState `json:"quality_5xx"`
	}
	if json.Unmarshal([]byte(raw), &e) != nil {
		return 0
	}
	return e.Quality.Attempt
}
