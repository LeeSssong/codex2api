package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
func (db *DB) RecordQuality5xxFailure(ctx context.Context, id int64, c smartops.OAuthAutoConfig, generation int64, revision string, models []string, effective ...int) (int, bool, error) {
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
		if ok, e := db.PluginEnabledTx(ctx, tx, smartops.PluginAutoConfig); e != nil || !ok {
			return e
		}
		if !c.UpgradeEnabled {
			return nil
		}
		groups, e := tx.QueryContext(ctx, `SELECT group_id FROM account_group_members WHERE account_id=$1`, id)
		if e != nil {
			return e
		}
		eligible := false
		for groups.Next() {
			var group int64
			if e = groups.Scan(&group); e != nil {
				groups.Close()
				return e
			}
			for _, wanted := range c.UpgradeGroupIDs {
				if group == wanted {
					eligible = true
				}
			}
		}
		e = groups.Err()
		groups.Close()
		if e != nil {
			return e
		}
		if !eligible {
			return nil
		}
		var current sql.NullInt64
		var raw string
		var actualGeneration int64
		q := `SELECT base_concurrency_override,credential_generation FROM accounts WHERE id=$1 AND status<>'deleted' AND enabled=TRUE AND type='oauth'`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		if err = tx.QueryRowContext(ctx, q, id).Scan(&current, &actualGeneration); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if actualGeneration != generation {
			return nil
		}
		if !current.Valid {
			fallback := c.Concurrency
			if len(effective) > 0 && effective[0] > 0 {
				fallback = effective[0]
			}
			current = sql.NullInt64{Int64: int64(fallback), Valid: true}
		}
		var state smartops.Quality5xxRampState
		if err = tx.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&raw); err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == sql.ErrNoRows {
			raw = ""
			err = nil
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
		if err = insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "upsert"); err != nil {
			return err
		}
		return tx.Commit()
	})
	return next, shouldProbe, err
}

func (db *DB) setModelCooldownTx(ctx context.Context, tx *sql.Tx, id int64, model, reason string, until time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO account_model_cooldowns(account_id,model,reason,reset_at,updated_at) VALUES($1,$2,$3,$4,CURRENT_TIMESTAMP) ON CONFLICT(account_id,model) DO UPDATE SET reason=excluded.reason,reset_at=excluded.reset_at,updated_at=CURRENT_TIMESTAMP WHERE account_model_cooldowns.reason=$3 OR account_model_cooldowns.reset_at <= CURRENT_TIMESTAMP`, id, model, reason, db.timeArg(until))
	return err
}

// ApplyQuality5xxProbe records a fenced probe result. Inconclusive and failed
// probes deliberately retain the ramp and cooldowns.
func (db *DB) ApplyQuality5xxProbe(ctx context.Context, id int64, c smartops.OAuthAutoConfig, generation int64, revision string, passed, conclusive bool, episode ...uint64) (int, bool, error) {
	var next int
	var active bool
	err := db.withSQLiteWriteLock(ctx, func() error {
		tx, err := db.conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		next, active, err = db.applyQuality5xxProbeTx(ctx, tx, id, c, generation, revision, passed, conclusive, episode...)
		if err != nil {
			return err
		}
		return tx.Commit()
	})
	return next, active, err
}
func (db *DB) applyQuality5xxProbeTx(ctx context.Context, tx *sql.Tx, id int64, c smartops.OAuthAutoConfig, generation int64, revision string, passed, conclusive bool, episode ...uint64) (next int, active bool, err error) {
	var current sql.NullInt64
	var raw string
	var actualGeneration int64
	q := `SELECT base_concurrency_override,credential_generation FROM accounts WHERE id=$1 AND status<>'deleted' AND enabled=TRUE AND type='oauth'`
	if !db.isSQLite() {
		q += ` FOR UPDATE`
	}
	if err = tx.QueryRowContext(ctx, q, id).Scan(&current, &actualGeneration); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return next, active, nil
		}
		return next, active, err
	}
	if actualGeneration != generation {
		return next, active, nil
	}
	if !current.Valid {
		current = sql.NullInt64{Int64: int64(c.Concurrency), Valid: true}
	}
	if err = tx.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&raw); err != nil {
		return next, active, err
	}
	var s smartops.Quality5xxRampState
	var envelope struct {
		Quality *smartops.Quality5xxRampState `json:"quality_5xx"`
	}
	if err = json.Unmarshal([]byte(raw), &envelope); err == nil && envelope.Quality != nil {
		s = *envelope.Quality
	} else if err = json.Unmarshal([]byte(raw), &s); err != nil {
		return next, active, err
	}
	if len(episode) == 0 || s.Attempt != episode[0] || !s.Active || !s.ProbePending || s.Generation != generation || s.Revision != revision || c.Revision != revision || int(current.Int64) != s.CurrentConcurrency {
		return next, active, nil
	}
	for _, plugin := range []string{smartops.PluginAutoConfig, "quality-ops"} {
		if ok, e := db.PluginEnabledTx(ctx, tx, plugin); e != nil || !ok {
			return next, active, e
		}
	}
	s, next = s.ProbeResult(c, int(current.Int64), generation, revision, passed, conclusive, time.Now())
	active = s.Active
	if !passed || !conclusive {
		for _, m := range s.OwnedModels {
			if _, err = tx.ExecContext(ctx, `UPDATE account_model_cooldowns SET reset_at=$1,updated_at=CURRENT_TIMESTAMP WHERE account_id=$2 AND model=$3 AND reason='quality_5xx_ramp'`, db.timeArg(time.Now().Add(time.Duration(c.Quality5xx.CooldownSeconds)*time.Second)), id, m); err != nil {
				return next, active, err
			}
		}
	}
	if next != int(current.Int64) {
		if _, err = tx.ExecContext(ctx, `UPDATE accounts SET base_concurrency_override=$1,updated_at=CURRENT_TIMESTAMP WHERE id=$2`, next, id); err != nil {
			return next, active, err
		}
		if err = insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "upsert"); err != nil {
			return next, active, err
		}
	}
	if passed && conclusive {
		for _, m := range s.OwnedModels {
			if _, err = tx.ExecContext(ctx, `DELETE FROM account_model_cooldowns WHERE account_id=$1 AND model=$2 AND reason='quality_5xx_ramp'`, id, m); err != nil {
				return next, active, err
			}
		}
		s.OwnedModels = nil
	}
	b := qualityPayload(raw, s)
	if _, err = tx.ExecContext(ctx, `UPDATE smart_ops_concurrency SET payload=$1 WHERE account_id=$2`, string(b), id); err != nil {
		return next, active, err
	}
	if err = insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "upsert"); err != nil {
		return next, active, err
	}
	return next, active, nil
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
	if !e.Quality.Active || !e.Quality.ProbePending {
		return 0
	}
	return e.Quality.Attempt
}
