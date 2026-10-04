package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/codex2api/smartops"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
	"sort"
	"strings"
	"time"
)

func (db *DB) EnsureSmartOpsSchema(ctx context.Context) error {
	db.smartOpsSchemaMu.Lock()
	defer db.smartOpsSchemaMu.Unlock()
	if db.smartOpsSchemaReady {
		return nil
	}
	id := "BIGSERIAL PRIMARY KEY"
	if db.isSQLite() {
		id = "INTEGER PRIMARY KEY AUTOINCREMENT"
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS smart_ops_settings(key TEXT PRIMARY KEY,value TEXT NOT NULL,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS smart_ops_test_jobs(id ` + id + `,plan_id BIGINT NOT NULL DEFAULT 0,payload TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'queued',owner TEXT NOT NULL DEFAULT '',lease_until BIGINT NOT NULL DEFAULT 0,created_at BIGINT NOT NULL,error TEXT NOT NULL DEFAULT '')`,
		`CREATE INDEX IF NOT EXISTS smart_ops_test_due ON smart_ops_test_jobs(status,lease_until,id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS smart_ops_test_one_active_plan ON smart_ops_test_jobs(plan_id) WHERE plan_id>0 AND status IN ('queued','running')`,
		`CREATE TABLE IF NOT EXISTS smart_ops_test_results(job_id BIGINT NOT NULL,sample INTEGER NOT NULL,account_id BIGINT NOT NULL,payload TEXT NOT NULL,output TEXT NOT NULL DEFAULT '',PRIMARY KEY(job_id,sample))`,
		`CREATE TABLE IF NOT EXISTS smart_ops_test_plans(id ` + id + `,payload TEXT NOT NULL,enabled BOOLEAN NOT NULL,next_run BIGINT NOT NULL,interval_minutes INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS smart_ops_concurrency(account_id BIGINT PRIMARY KEY,payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS smart_ops_plugin_epochs(plugin_id TEXT PRIMARY KEY,epoch BIGINT NOT NULL DEFAULT 0)`,
	} {
		if _, e := db.conn.ExecContext(ctx, q); e != nil {
			return e
		}
	}
	db.smartOpsSchemaReady = true
	return nil
}
func (db *DB) GetSmartOpsConfig(ctx context.Context, key string, target any) error {
	if e := db.EnsureSmartOpsSchema(ctx); e != nil {
		return e
	}
	var raw string
	e := db.conn.QueryRowContext(ctx, `SELECT value FROM smart_ops_settings WHERE key=$1`, key).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	return json.Unmarshal([]byte(raw), target)
}
func (db *DB) PutSmartOpsConfig(ctx context.Context, key string, value any) error {
	if e := db.EnsureSmartOpsSchema(ctx); e != nil {
		return e
	}
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		plugin := smartops.PluginAutoConfig
		if key == "priority_scheduling" {
			plugin = smartops.PluginPriorityScheduling
		}
		enabled, e := db.PluginEnabledTx(ctx, tx, plugin)
		if e != nil {
			return e
		}
		if !enabled {
			return smartops.ErrDisabled
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO smart_ops_settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=CURRENT_TIMESTAMP`, key, string(b))
		return e
	})
}
func (db *DB) LoadOAuthAutoConfig(ctx context.Context) (smartops.OAuthAutoConfig, error) {
	c := smartops.DefaultOAuthAutoConfig()
	e := db.GetSmartOpsConfig(ctx, "oauth_auto_config", &c)
	return c, e
}
func (db *DB) SaveOAuthAutoConfig(ctx context.Context, c smartops.OAuthAutoConfig) error {
	if e := smartops.ValidateOAuthAutoConfig(c); e != nil {
		return e
	}
	if c.Enabled {
		groups, e := db.ListAccountGroups(ctx)
		if e != nil {
			return e
		}
		valid := map[int64]bool{}
		channel := "codex"
		if c.Platform == "claude" {
			channel = "claude"
		}
		if c.Platform == "grok" {
			channel = "grok"
		}
		if c.Platform == "antigravity" {
			channel = "antigravity"
		}
		for _, g := range groups {
			if g.Channel == channel {
				valid[g.ID] = true
			}
		}
		for _, id := range c.GroupIDs {
			if !valid[id] {
				return errors.New("group missing or belongs to another platform")
			}
		}
	}
	if c.UpgradeEnabled {
		valid, e := db.VerifyAccountGroupIDs(ctx, c.UpgradeGroupIDs)
		if e != nil {
			return e
		}
		if len(valid) != 0 {
			return errors.New("upgrade group missing")
		}
	}
	return db.PutSmartOpsConfig(ctx, "oauth_auto_config", c)
}

func (db *DB) SnapshotSmartOpsBilling(input *UsageLogInput) *UsageLogInput {
	if input == nil || input.APIKeyID <= 0 {
		return input
	}
	if input.billingSnapshot != nil && input.billingSnapshot.smartOpsApplied {
		return input
	}
	policy := db.smartOpsBilling.Load()
	input = SnapshotUsageLogBilling(input)
	if input.billingSnapshot != nil && IsUnitUserBillingMode(input.billingSnapshot.UserBillingMode) {
		return input
	}
	model := input.EffectiveModel
	if model == "" {
		model = input.Model
	}
	multiplier := 1.0
	if policy != nil && policy.gate != nil && policy.gate() {
		multiplier = policy.config.Multiplier(model)
	}
	copy := *input
	var snapshot usageBillingSnapshot
	if input.billingSnapshot != nil {
		snapshot = *input.billingSnapshot
	} else {
		snapshot = usageBillingSnapshot{UserBilling: UserBilling{UserBillingMode: UserBillingModeToken}, accountCost: UsageLogBilledCost(input)}
	}
	snapshot.modelMultiplier = multiplier
	snapshot.smartOpsApplied = true
	copy.billingSnapshot = &snapshot
	return &copy
}

type smartOpsBillingPolicy struct {
	config smartops.ModelBillingConfig
	gate   func() bool
}

func (db *DB) SetSmartOpsBilling(config smartops.ModelBillingConfig, gate func() bool) {
	config.Rules = append([]smartops.ModelBillingRule{}, config.Rules...)
	db.smartOpsBilling.Store(&smartOpsBillingPolicy{config: config, gate: gate})
}
func (db *DB) LoadPriorityScheduling(ctx context.Context) (smartops.PriorityConfig, error) {
	c := smartops.DefaultPriorityConfig()
	e := db.GetSmartOpsConfig(ctx, "priority_scheduling", &c)
	return c, e
}
func (db *DB) SavePriorityScheduling(ctx context.Context, c smartops.PriorityConfig) error {
	if e := smartops.ValidatePriorityConfig(c); e != nil {
		return e
	}
	return db.PutSmartOpsConfig(ctx, "priority_scheduling", c)
}

func (db *DB) insertPelicanTx(ctx context.Context, tx *sql.Tx, j smartops.PelicanJob, plan int64) (smartops.JobRecord, error) {
	enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginPelicanTests)
	if e != nil {
		return smartops.JobRecord{}, e
	}
	if !enabled {
		return smartops.JobRecord{}, smartops.ErrDisabled
	}
	j.ID = 0
	raw, e := json.Marshal(j)
	if e != nil {
		return smartops.JobRecord{}, e
	}
	rec := smartops.JobRecord{PelicanJob: j, PlanID: plan, Status: "queued", CreatedAt: time.Now().UTC(), Results: []smartops.PelicanResult{}}
	e = tx.QueryRowContext(ctx, `INSERT INTO smart_ops_test_jobs(plan_id,payload,created_at) VALUES($1,$2,$3) RETURNING id`, plan, string(raw), rec.CreatedAt.UnixMilli()).Scan(&rec.ID)
	return rec, e
}
func (db *DB) CreatePelicanJob(ctx context.Context, j smartops.PelicanJob, plan int64) (smartops.JobRecord, error) {
	if e := smartops.ValidatePelicanJob(j); e != nil {
		return smartops.JobRecord{}, e
	}
	if e := db.EnsureSmartOpsSchema(ctx); e != nil {
		return smartops.JobRecord{}, e
	}
	tx, e := db.conn.BeginTx(ctx, nil)
	if e != nil {
		return smartops.JobRecord{}, e
	}
	defer tx.Rollback()
	r, e := db.insertPelicanTx(ctx, tx, j, plan)
	if e != nil {
		return r, e
	}
	return r, tx.Commit()
}

type pelicanScanner interface{ Scan(...any) error }

func scanPelicanJob(row pelicanScanner) (smartops.JobRecord, error) {
	var j smartops.JobRecord
	var raw string
	var created int64
	e := row.Scan(&j.ID, &j.PlanID, &raw, &j.Status, &created, &j.Error)
	if e != nil {
		return j, e
	}
	id := j.ID
	if e = json.Unmarshal([]byte(raw), &j.PelicanJob); e != nil {
		return j, e
	}
	j.ID = id
	j.CreatedAt = time.UnixMilli(created).UTC()
	j.Results = []smartops.PelicanResult{}
	return j, nil
}
func (db *DB) ListPelicanJobs(ctx context.Context) ([]smartops.JobRecord, error) {
	if e := db.EnsureSmartOpsSchema(ctx); e != nil {
		return nil, e
	}
	rows, e := db.conn.QueryContext(ctx, `SELECT id,plan_id,payload,status,created_at,error FROM smart_ops_test_jobs ORDER BY id DESC LIMIT 100`)
	if e != nil {
		return nil, e
	}
	out := []smartops.JobRecord{}
	for rows.Next() {
		j, e := scanPelicanJob(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, j)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range out {
		rows, e := db.conn.QueryContext(ctx, `SELECT payload FROM smart_ops_test_results WHERE job_id=$1 ORDER BY sample`, out[i].ID)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var raw string
			var r smartops.PelicanResult
			if e = rows.Scan(&raw); e == nil {
				e = json.Unmarshal([]byte(raw), &r)
			}
			if e != nil {
				rows.Close()
				return nil, e
			}
			out[i].Results = append(out[i].Results, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
func (db *DB) ClaimPelicanJob(ctx context.Context, owner string, now time.Time) (*smartops.JobRecord, error) {
	tx, e := db.conn.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginPelicanTests)
	if e != nil {
		return nil, e
	}
	if !enabled {
		return nil, nil
	}
	q := `SELECT id,plan_id,payload,status,created_at,error FROM smart_ops_test_jobs WHERE status='queued' OR (status='running' AND lease_until<$1) ORDER BY id LIMIT 1`
	if !db.isSQLite() {
		q += ` FOR UPDATE SKIP LOCKED`
	}
	j, e := scanPelicanJob(tx.QueryRowContext(ctx, q, now.UnixMilli()))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	res, e := tx.ExecContext(ctx, `UPDATE smart_ops_test_jobs SET status='running',owner=$1,lease_until=$2 WHERE id=$3 AND (status='queued' OR (status='running' AND lease_until<$4))`, owner, now.Add(30*time.Second).UnixMilli(), j.ID, now.UnixMilli())
	if e != nil {
		return nil, e
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	j.Status = "running"
	detail, e := db.GetPelicanJob(ctx, j.ID)
	if e != nil {
		return nil, e
	}
	detail.Status = "running"
	return &detail, nil
}
func (db *DB) PelicanJobActive(ctx context.Context, id int64, owner string, now time.Time) (bool, error) {
	active := false
	e := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginPelicanTests)
		if e != nil {
			return e
		}
		if !enabled {
			return nil
		}
		res, e := tx.ExecContext(ctx, `UPDATE smart_ops_test_jobs SET lease_until=$1 WHERE id=$2 AND owner=$3 AND status='running' AND lease_until>=$4`, now.Add(30*time.Second).UnixMilli(), id, owner, now.UnixMilli())
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		active = n == 1
		return e
	})
	return active, e
}
func (db *DB) FinishPelicanJob(ctx context.Context, j smartops.JobRecord, owner string) error {
	tx, e := db.conn.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginPelicanTests)
	if e != nil {
		return e
	}
	if !enabled {
		j.Status = "cancelled"
		j.Error = "plugin disabled"
	}
	res, e := tx.ExecContext(ctx, `UPDATE smart_ops_test_jobs SET status=CASE WHEN status='cancelled' THEN status ELSE $1 END,error=$2,owner='',lease_until=0 WHERE id=$3 AND owner=$4 AND status IN ('running','cancelled')`, j.Status, j.Error, j.ID, owner)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("test lease lost before finalization")
	}
	keep := j.MaxHistory
	if keep < 1 {
		keep = 100
	}
	old := `SELECT id FROM smart_ops_test_jobs WHERE plan_id=$1 AND status IN ('completed','failed','cancelled','interrupted') AND owner='' ORDER BY id DESC LIMIT 10000000 OFFSET $2`
	if _, e = tx.ExecContext(ctx, `DELETE FROM smart_ops_test_results WHERE job_id IN (`+old+`)`, j.PlanID, keep); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM smart_ops_test_jobs WHERE id IN (`+old+`)`, j.PlanID, keep); e != nil {
		return e
	}
	return tx.Commit()
}

func (db *DB) GetPelicanJob(ctx context.Context, id int64) (smartops.JobRecord, error) {
	j, e := scanPelicanJob(db.conn.QueryRowContext(ctx, `SELECT id,plan_id,payload,status,created_at,error FROM smart_ops_test_jobs WHERE id=$1`, id))
	if e != nil {
		return j, e
	}
	rows, e := db.conn.QueryContext(ctx, `SELECT payload,output FROM smart_ops_test_results WHERE job_id=$1 ORDER BY sample`, id)
	if e != nil {
		return j, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw, output string
		var result smartops.PelicanResult
		if e = rows.Scan(&raw, &output); e == nil {
			e = json.Unmarshal([]byte(raw), &result)
		}
		if e != nil {
			return j, e
		}
		result.Output = output
		j.Results = append(j.Results, result)
	}
	return j, rows.Err()
}
func (db *DB) CancelPelicanJob(ctx context.Context, id int64) error {
	r, e := db.conn.ExecContext(ctx, `UPDATE smart_ops_test_jobs SET status='cancelled' WHERE id=$1 AND status IN ('queued','running')`, id)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e == nil && n == 0 {
		return sql.ErrNoRows
	}
	return e
}
func (db *DB) SavePelicanResult(ctx context.Context, r smartops.PelicanResult) error {
	if r.LeaseOwner == "" {
		return errors.New("test lease required")
	}
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginPelicanTests)
		if e != nil {
			return e
		}
		q := `SELECT owner,status,lease_until FROM smart_ops_test_jobs WHERE id=$1`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		var owner, status string
		var until int64
		if e = tx.QueryRowContext(ctx, q, r.JobID).Scan(&owner, &status, &until); e != nil {
			return e
		}
		if owner != r.LeaseOwner || status != "running" && status != "cancelled" || status == "running" && until < time.Now().UnixMilli() {
			return errors.New("test lease lost before result publication")
		}
		if !enabled || status == "cancelled" {
			r.Status = "cancelled"
			if r.Error == "" {
				r.Error = "test cancelled"
			}
			if _, e = tx.ExecContext(ctx, `UPDATE smart_ops_test_jobs SET status='cancelled',lease_until=0 WHERE id=$1`, r.JobID); e != nil {
				return e
			}
		}
		output := r.Output
		r.Output = ""
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO smart_ops_test_results(job_id,sample,account_id,payload,output) VALUES($1,$2,$3,$4,$5) ON CONFLICT(job_id,sample) DO UPDATE SET account_id=EXCLUDED.account_id,payload=EXCLUDED.payload,output=EXCLUDED.output`, r.JobID, r.Sample, r.AccountID, string(b), output)
		return e
	})
}
func (db *DB) ListPelicanPlans(ctx context.Context) ([]smartops.PelicanPlan, error) {
	if e := db.EnsureSmartOpsSchema(ctx); e != nil {
		return nil, e
	}
	rows, e := db.conn.QueryContext(ctx, `SELECT id,payload,enabled,next_run,interval_minutes FROM smart_ops_test_plans ORDER BY id DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []smartops.PelicanPlan{}
	for rows.Next() {
		var p smartops.PelicanPlan
		var raw string
		var next int64
		var id int64
		if e = rows.Scan(&id, &raw, &p.Enabled, &next, &p.IntervalMinutes); e != nil {
			return nil, e
		}
		enabled, interval := p.Enabled, p.IntervalMinutes
		if e = json.Unmarshal([]byte(raw), &p); e != nil {
			return nil, e
		}
		p.ID = id
		p.Enabled = enabled
		p.IntervalMinutes = interval
		p.NextRunAt = time.UnixMilli(next).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}
func (db *DB) SavePelicanPlan(ctx context.Context, p smartops.PelicanPlan) (smartops.PelicanPlan, error) {
	if e := db.EnsureSmartOpsSchema(ctx); e != nil {
		return p, e
	}
	b, e := json.Marshal(p)
	if e != nil {
		return p, e
	}
	e = db.withWriteTx(ctx, func(tx *sql.Tx) error {
		enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginPelicanTests)
		if e != nil {
			return e
		}
		if !enabled {
			return smartops.ErrDisabled
		}
		if p.ID == 0 {
			return tx.QueryRowContext(ctx, `INSERT INTO smart_ops_test_plans(payload,enabled,next_run,interval_minutes) VALUES($1,$2,$3,$4) RETURNING id`, string(b), p.Enabled, p.NextRunAt.UnixMilli(), p.IntervalMinutes).Scan(&p.ID)
		}
		res, e := tx.ExecContext(ctx, `UPDATE smart_ops_test_plans SET payload=$1,enabled=$2,next_run=$3,interval_minutes=$4 WHERE id=$5`, string(b), p.Enabled, p.NextRunAt.UnixMilli(), p.IntervalMinutes, p.ID)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e == nil && n == 0 {
			return sql.ErrNoRows
		}
		return e
	})
	return p, e
}
func (db *DB) DeletePelicanPlan(ctx context.Context, id int64) error {
	_, e := db.conn.ExecContext(ctx, `DELETE FROM smart_ops_test_plans WHERE id=$1`, id)
	return e
}
func (db *DB) EnqueueDuePelicanPlans(ctx context.Context, now time.Time) error {
	ps, e := db.ListPelicanPlans(ctx)
	if e != nil {
		return e
	}
	for _, p := range ps {
		if !p.Enabled || p.NextRunAt.After(now) {
			continue
		}
		if e = db.enqueueDuePelicanPlan(ctx, p.ID, now); e != nil {
			return e
		}
	}
	return nil
}

func (db *DB) enqueueDuePelicanPlan(ctx context.Context, id int64, now time.Time) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginPelicanTests)
		if e != nil {
			return e
		}
		if !enabled {
			return nil
		}
		q := `SELECT payload,enabled,next_run,interval_minutes FROM smart_ops_test_plans WHERE id=$1`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		var raw string
		var due int64
		var interval int
		var on bool
		e = tx.QueryRowContext(ctx, q, id).Scan(&raw, &on, &due, &interval)
		if e == sql.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		if !on || due > now.UnixMilli() {
			return nil
		}
		var plan smartops.PelicanPlan
		if e = json.Unmarshal([]byte(raw), &plan); e != nil {
			return e
		}
		var active int
		if e = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM smart_ops_test_jobs WHERE plan_id=$1 AND status IN ('queued','running')`, id).Scan(&active); e != nil {
			return e
		}
		if active > 0 {
			return nil
		}
		next := now.Add(time.Duration(interval) * time.Minute)
		if plan.CronExpression != "" {
			schedule, e := cron.ParseStandard(plan.CronExpression)
			if e != nil {
				return e
			}
			next = schedule.Next(now)
		}
		if _, e = tx.ExecContext(ctx, `UPDATE smart_ops_test_plans SET next_run=$1 WHERE id=$2`, next.UnixMilli(), id); e != nil {
			return e
		}
		_, e = db.insertPelicanTx(ctx, tx, plan.Job, id)
		return e
	})
}

func (db *DB) RecordSmartOpsConcurrency(ctx context.Context, id int64, c smartops.OAuthAutoConfig, success bool, observedEpoch ...int64) (int64, bool, error) {
	if !c.UpgradeEnabled {
		return 0, false, nil
	}
	if e := db.EnsureSmartOpsSchema(ctx); e != nil {
		return 0, false, e
	}
	var next int64
	var changed bool
	e := db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		enabled, e := db.PluginEnabledTx(ctx, tx, smartops.PluginAutoConfig)
		if e != nil {
			return e
		}
		if !enabled {
			return nil
		}
		if len(observedEpoch) > 0 {
			var epoch int64
			e = tx.QueryRowContext(ctx, `SELECT epoch FROM smart_ops_plugin_epochs WHERE plugin_id=$1`, smartops.PluginAutoConfig).Scan(&epoch)
			if e != nil && e != sql.ErrNoRows {
				return e
			}
			if epoch != observedEpoch[0] {
				return nil
			}
		}
		q := `SELECT base_concurrency_override,credentials FROM accounts WHERE id=$1 AND status<>'deleted'`
		if !db.isSQLite() {
			q += ` FOR UPDATE`
		}
		var current sql.NullInt64
		var raw any
		if e = tx.QueryRowContext(ctx, q, id).Scan(&current, &raw); e != nil {
			return e
		}
		_ = decodeCredentials(raw)
		if !current.Valid {
			return nil
		}
		groups, e := tx.QueryContext(ctx, `SELECT group_id FROM account_group_members WHERE account_id=$1`, id)
		if e != nil {
			return e
		}
		eligible := false
		for groups.Next() {
			var gid int64
			if e = groups.Scan(&gid); e != nil {
				groups.Close()
				return e
			}
			for _, wanted := range c.UpgradeGroupIDs {
				if wanted == gid {
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
		var state smartops.ConcurrencyState
		var payload string
		e = tx.QueryRowContext(ctx, `SELECT payload FROM smart_ops_concurrency WHERE account_id=$1`, id).Scan(&payload)
		var quality smartops.Quality5xxRampState
		if e == nil {
			var envelope struct {
				Quality *smartops.Quality5xxRampState `json:"quality_5xx"`
			}
			if json.Unmarshal([]byte(payload), &envelope) == nil && envelope.Quality != nil {
				quality = *envelope.Quality
			}
			var env map[string]json.RawMessage
			_ = json.Unmarshal([]byte(payload), &env)
			if rawState, ok := env["concurrency"]; ok {
				if e = json.Unmarshal(rawState, &state); e != nil {
					return e
				}
			} else if e = json.Unmarshal([]byte(payload), &state); e != nil {
				return e
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var n int
		if quality.Active && quality.CurrentConcurrency != int(current.Int64) {
			// An explicit concurrency edit relinquishes recovery ownership.
			quality.Active = false
			quality.ProbePending = false
		}
		if quality.Active && quality.ProbePending {
			n = int(current.Int64)
		} else if quality.Active {
			recoveryConfig := c
			if quality.OriginalConcurrency < recoveryConfig.MaxConcurrency {
				recoveryConfig.MaxConcurrency = quality.OriginalConcurrency
			}
			quality.Concurrency, n = smartops.AdvanceConcurrency(quality.Concurrency, int(current.Int64), recoveryConfig, smartops.ConcurrencyResult{Success: success, At: time.Now()}, time.Now())
			quality.CurrentConcurrency = n
			state = quality.Concurrency
			if n >= recoveryConfig.MaxConcurrency {
				quality.Active = false
			}
		} else {
			if quality.Attempt > 0 && quality.Revision == c.Revision && quality.CurrentConcurrency == int(current.Int64) && quality.OriginalConcurrency > 0 && c.MaxConcurrency > quality.OriginalConcurrency {
				c.MaxConcurrency = quality.OriginalConcurrency
			}
			state, n = smartops.AdvanceConcurrency(state, int(current.Int64), c, smartops.ConcurrencyResult{Success: success, At: time.Now()}, time.Now())
		}
		m := map[string]json.RawMessage{}
		if payload != "" {
			_ = json.Unmarshal([]byte(payload), &m)
		}
		sb, _ := json.Marshal(state)
		m["concurrency"] = sb
		if quality.Active || quality.Attempt > 0 {
			qb, _ := json.Marshal(quality)
			m["quality_5xx"] = qb
		}
		b, _ := json.Marshal(m)
		if _, e = tx.ExecContext(ctx, `INSERT INTO smart_ops_concurrency(account_id,payload) VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET payload=EXCLUDED.payload`, id, string(b)); e != nil {
			return e
		}
		next = int64(n)
		changed = next != current.Int64
		if changed {
			if _, e = tx.ExecContext(ctx, `UPDATE accounts SET base_concurrency_override=$1,updated_at=CURRENT_TIMESTAMP WHERE id=$2`, next, id); e != nil {
				return e
			}
			if e = insertSchedulerOutboxEventTx(ctx, tx, SchedulerEntityAccount, id, "upsert"); e != nil {
				return e
			}
		}
		return tx.Commit()
	})
	return next, changed, e
}

// Background aggregation, never called from request selection.
func (db *DB) ReadSmartOpsSignals(ctx context.Context, c smartops.PriorityConfig) (map[int64]smartops.Signal, error) {
	q := `SELECT account_id,COUNT(*),SUM(CASE WHEN status_code>=200 AND status_code<400 AND COALESCE(error_message,'')='' THEN 1 ELSE 0 END),AVG(NULLIF(first_token_ms,0)),SUM(account_billed) FROM usage_logs WHERE created_at>=$1 GROUP BY account_id`
	if !db.isSQLite() {
		q = `SELECT account_id,COUNT(*),SUM(CASE WHEN status_code>=200 AND status_code<400 AND COALESCE(error_message,'')='' THEN 1 ELSE 0 END),percentile_cont(0.9) WITHIN GROUP (ORDER BY NULLIF(first_token_ms,0)),SUM(account_billed) FROM usage_logs WHERE created_at>=$1 GROUP BY account_id`
	}
	rows, e := db.conn.QueryContext(ctx, q, db.timeArg(time.Now().Add(-time.Duration(c.WindowMinutes)*time.Minute)))
	if e != nil {
		return nil, e
	}
	out := map[int64]smartops.Signal{}
	for rows.Next() {
		var id int64
		var n, success int
		var ttft, cost sql.NullFloat64
		if e = rows.Scan(&id, &n, &success, &ttft, &cost); e != nil {
			return nil, e
		}
		out[id] = smartops.Signal{Samples: n, QualityPercent: float64(success) * 100 / float64(n), P90TTFTMs: ttft.Float64, Cost: cost.Float64 / float64(n)}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if db.isSQLite() {
		rs, e := db.conn.QueryContext(ctx, `SELECT account_id,first_token_ms FROM usage_logs WHERE created_at>=$1 AND first_token_ms>0 ORDER BY created_at DESC LIMIT 100000`, sqliteTimeParam(time.Now().Add(-time.Duration(c.WindowMinutes)*time.Minute)))
		if e != nil {
			return nil, e
		}
		values := map[int64][]int{}
		for rs.Next() {
			var id int64
			var ms int
			if e = rs.Scan(&id, &ms); e != nil {
				rs.Close()
				return nil, e
			}
			values[id] = append(values[id], ms)
		}
		e = rs.Err()
		rs.Close()
		if e != nil {
			return nil, e
		}
		for id, v := range values {
			sort.Ints(v)
			signal := out[id]
			index := (len(v)*9+9)/10 - 1
			signal.P90TTFTMs = float64(v[index])
			out[id] = signal
		}
	}
	if e = db.mergeSmartOpsQualitySignals(ctx, c, out); e != nil {
		return nil, e
	}
	return out, nil
}

// Creation, credentials, scheduling metadata and group bindings commit together.
func (db *DB) InsertAutoConfiguredOAuthAccount(ctx context.Context, name, platform, accountType string, credentials map[string]interface{}, proxyURL string, c smartops.OAuthAutoConfig) (int64, error) {
	if e := smartops.ValidateOAuthAutoConfig(c); e != nil {
		return 0, e
	}
	credentials = cloneCredentialUpdates(credentials)
	b, e := json.Marshal(encryptSensitiveCredentials(credentials))
	if e != nil {
		return 0, e
	}
	family := credentialFamilyCandidate(credentials)
	if family == "" {
		family = "cf_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	var id int64
	e = db.withSQLiteWriteLock(ctx, func() error {
		tx, e := db.conn.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = tx.QueryRowContext(ctx, `INSERT INTO accounts(name,platform,type,credentials,proxy_url,credential_family_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, name, platform, accountType, b, proxyURL, family).Scan(&id); e != nil {
			return e
		}
		if e = db.applySmartOpsDefaultsTx(ctx, tx, id, c); e != nil {
			return e
		}
		return tx.Commit()
	})
	if e != nil {
		return 0, e
	}
	return id, nil
}

var _ smartops.Store = (*DB)(nil)
