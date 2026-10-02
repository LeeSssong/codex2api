package database

import (
	"context"
	"database/sql"
	"github.com/codex2api/plugins"
	"github.com/codex2api/smartops"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func smartOpsDB(t *testing.T) *DB {
	t.Helper()
	driver, dsn := "sqlite", filepath.Join(t.TempDir(), "smart.db")
	if v := os.Getenv("SMARTOPS_TEST_POSTGRES_DSN"); v != "" {
		driver, dsn = "postgres", v
	}
	schema := "smartops_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if driver == "postgres" {
		dsn += "&search_path=" + schema
	}
	db, e := New(driver, dsn, schema)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = db.EnsureSmartOpsSchema(context.Background()); e != nil {
		t.Fatal(e)
	}
	return db
}

func TestSmartOpsPluginDisableABAFencesResultsAndObservations(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	controls := NewPluginStore(db)
	job, e := db.CreatePelicanJob(ctx, smartops.PelicanJob{AccountID: 1, Model: "test", Samples: 1, Parallel: 1}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.ClaimPelicanJob(ctx, "old-worker", time.Now()); e != nil {
		t.Fatal(e)
	}
	for _, on := range []bool{false, true} {
		if e = controls.Put(ctx, plugins.Setting{ID: smartops.PluginPelicanTests, Enabled: on}); e != nil {
			t.Fatal(e)
		}
	}
	active, e := db.PelicanJobActive(ctx, job.ID, "old-worker", time.Now())
	if e != nil || active {
		t.Fatalf("off/on revived running lease: %v %v", active, e)
	}
	if e = db.SavePelicanResult(ctx, smartops.PelicanResult{JobID: job.ID, AccountID: 1, Sample: 0, LeaseOwner: "old-worker", Status: "success", Output: "<html>paid old execution</html>"}); e != nil {
		t.Fatal(e)
	}
	record, e := db.GetPelicanJob(ctx, job.ID)
	if e != nil || record.Status != "cancelled" || record.Results[0].Status != "cancelled" {
		t.Fatalf("cancelled work published success: %+v %v", record, e)
	}
	group, e := db.CreateAccountGroup(ctx, "upgrades", "", "#334455", 0, 0, sql.NullInt64{})
	if e != nil {
		t.Fatal(e)
	}
	cfg := smartops.DefaultOAuthAutoConfig()
	cfg.Enabled = true
	cfg.UpgradeEnabled = true
	cfg.GroupIDs = []int64{group}
	cfg.UpgradeGroupIDs = []int64{group}
	cfg.SuccessesPerStep = 1
	id, e := db.InsertAutoConfiguredOAuthAccount(ctx, "native", "openai", "oauth", map[string]interface{}{"access_token": "test"}, "", cfg)
	if e != nil {
		t.Fatal(e)
	}
	for _, on := range []bool{false, true} {
		if e = controls.Put(ctx, plugins.Setting{ID: smartops.PluginAutoConfig, Enabled: on}); e != nil {
			t.Fatal(e)
		}
	}
	_, changed, e := db.RecordSmartOpsConcurrency(ctx, id, cfg, true, 0)
	if e != nil || changed {
		t.Fatalf("old observation survived off/on: %v %v", changed, e)
	}
	_, changed, e = db.RecordSmartOpsConcurrency(ctx, id, cfg, true, 1)
	if e != nil || !changed {
		t.Fatalf("fresh observation failed: %v %v", changed, e)
	}
}
func TestSmartOpsNeutralBillingSnapshotCannotChangeAfterEnable(t *testing.T) {
	db := smartOpsDB(t)
	var on atomic.Bool
	cfg := smartops.ModelBillingConfig{Enabled: true, Rules: []smartops.ModelBillingRule{{Model: "gpt-4o-mini", Multiplier: 2}}}
	db.SetSmartOpsBilling(cfg, on.Load)
	input := &UsageLogInput{APIKeyID: 1, Model: "gpt-4o-mini", InputTokens: 1000000}
	snapshot := db.SnapshotSmartOpsBilling(input)
	neutral := UsageLogUserBilledCost(snapshot)
	on.Store(true)
	if UsageLogUserBilledCost(db.SnapshotSmartOpsBilling(snapshot)) != neutral {
		t.Fatal("customer charge changed after scope accounting snapshot")
	}
}
func TestSmartOpsAntigravityDefaultsValidateAndPublishNativeGroups(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	g, e := db.CreateAccountGroup(ctx, "AG", "", "#334455", 0, 0, sql.NullInt64{})
	if e != nil {
		t.Fatal(e)
	}
	channel := "antigravity"
	if e = db.UpdateAccountGroup(ctx, g, nil, nil, nil, &UpdateAccountGroupOpts{Channel: &channel}); e != nil {
		t.Fatal(e)
	}
	cfg := smartops.DefaultOAuthAutoConfig()
	cfg.Enabled = true
	cfg.Platform = "antigravity"
	cfg.GroupIDs = []int64{g}
	if e = db.SaveOAuthAutoConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	db.SetSmartOpsOAuthDefaultsProvider(func(context.Context) (smartops.OAuthAutoConfig, bool) { return cfg, true })
	id, e := db.InsertAccountWithUpstream(ctx, "new AG", "google", "antigravity", map[string]interface{}{"upstream_type": "antigravity", "refresh_token": "synthetic"}, "")
	if e != nil {
		t.Fatal(e)
	}
	groups, e := db.GetAccountGroupIDs(ctx, id)
	if e != nil || len(groups) != 1 || groups[0] != g {
		t.Fatalf("AG defaults missing: %v %v", groups, e)
	}
}
func TestSmartOpsDurableClaimCancelAndHistory(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	j, e := db.CreatePelicanJob(ctx, smartops.PelicanJob{AccountID: 1, Model: "test", Samples: 1, Parallel: 1}, 0)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var claimed atomic.Int32
	for _, owner := range []string{"first", "second"} {
		wg.Add(1)
		go func(owner string) {
			defer wg.Done()
			r, e := db.ClaimPelicanJob(ctx, owner, time.Now())
			if e != nil {
				t.Error(e)
			}
			if r != nil {
				claimed.Add(1)
			}
		}(owner)
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("claimed %d times", claimed.Load())
	}
	if e = db.CancelPelicanJob(ctx, j.ID); e != nil {
		t.Fatal(e)
	}
	if r, e := db.ClaimPelicanJob(ctx, "restart", time.Now().Add(time.Minute)); e != nil || r != nil {
		t.Fatalf("cancelled job reclaimed: %v %v", r, e)
	}
	var owner string
	if e = db.conn.QueryRowContext(ctx, `SELECT owner FROM smart_ops_test_jobs WHERE id=$1`, j.ID).Scan(&owner); e != nil {
		t.Fatal(e)
	}
	if e = db.SavePelicanResult(ctx, smartops.PelicanResult{LeaseOwner: owner, JobID: j.ID, AccountID: 1, Sample: 0, Status: "success", Output: "<html>durable</html>"}); e != nil {
		t.Fatal(e)
	}
	jobs, e := db.ListPelicanJobs(ctx)
	if e != nil || len(jobs) != 1 || jobs[0].Status != "cancelled" || len(jobs[0].Results) != 1 {
		t.Fatalf("history=%+v err=%v", jobs, e)
	}
}
func TestSmartOpsPlanDueIsAtomic(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	p, e := db.SavePelicanPlan(ctx, smartops.PelicanPlan{Name: "scheduled", Enabled: true, IntervalMinutes: 60, NextRunAt: time.Now().Add(-time.Minute), Job: smartops.PelicanJob{AccountID: 1, Model: "test", Samples: 1, Parallel: 1}})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.EnqueueDuePelicanPlans(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	if e = db.EnqueueDuePelicanPlans(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	jobs, e := db.ListPelicanJobs(ctx)
	if e != nil || len(jobs) != 1 || jobs[0].PlanID != p.ID {
		t.Fatalf("jobs=%+v err=%v", jobs, e)
	}
	plans, e := db.ListPelicanPlans(ctx)
	if e != nil || !plans[0].NextRunAt.After(time.Now()) {
		t.Fatalf("plans=%v err=%v", plans, e)
	}
}
func TestSmartOpsConcurrencyPersistsEverySuccessAndManualChange(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	g, e := db.CreateAccountGroup(ctx, "managed", "", "#345678", 0, 0, sql.NullInt64{})
	if e != nil {
		t.Fatal(e)
	}
	c := smartops.DefaultOAuthAutoConfig()
	c.Enabled = true
	c.UpgradeEnabled = true
	c.GroupIDs = []int64{g}
	c.UpgradeGroupIDs = []int64{g}
	c.SuccessesPerStep = 2
	c.MaxConcurrency = 4
	c.Revision = "one"
	id, e := db.InsertAutoConfiguredOAuthAccount(ctx, "initial", "openai", "oauth", map[string]interface{}{"access_token": "synthetic"}, "", c)
	if e != nil {
		t.Fatal(e)
	}
	watermark, e := db.SchedulerOutboxHighWatermark(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		next, changed, e := db.RecordSmartOpsConcurrency(ctx, id, c, true)
		if e != nil {
			t.Fatal(e)
		}
		if i == 0 && changed || i == 1 && (!changed || next != 4) {
			t.Fatalf("step=%d next=%d changed=%v", i, next, changed)
		}
	}
	row, e := db.GetAccountByID(ctx, id)
	if e != nil || row.BaseConcurrencyOverride.Int64 != 4 {
		t.Fatalf("concurrency=%+v err=%v", row, e)
	}
	events, e := db.ListSchedulerOutboxEventsAfter(ctx, watermark, 100)
	if e != nil || len(events) == 0 {
		t.Fatalf("no publication event: %v", e)
	}
	if _, changed, e := db.RecordSmartOpsConcurrency(ctx, id, c, true); e != nil || changed {
		t.Fatalf("cap bypass: %v %v", changed, e)
	}
	c.Enabled = false
	if _, _, e = db.RecordSmartOpsConcurrency(ctx, id, c, false); e != nil {
		t.Fatal(e)
	}
}
func TestSmartOpsRuntimeUsesServerContextAndCancelsNativeExecutor(t *testing.T) {
	db := smartOpsDB(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var enabled atomic.Bool
	enabled.Store(true)
	r := smartops.NewRuntime(func(context.Context, string) bool { return enabled.Load() })
	started := make(chan struct{})
	stopped := make(chan struct{})
	if e := r.Start(parent, db, func(ctx context.Context, j smartops.PelicanJob, i int) (smartops.PelicanResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return smartops.PelicanResult{AccountID: j.AccountID}, ctx.Err()
	}); e != nil {
		t.Fatal(e)
	}
	request, rCancel := context.WithCancel(context.Background())
	job, e := r.CreateJob(request, smartops.PelicanJob{AccountID: 1, Model: "test", Samples: 1, Parallel: 1})
	if e != nil {
		t.Fatal(e)
	}
	rCancel()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("executor never called")
	}
	select {
	case <-stopped:
		t.Fatal("request cancellation killed server-owned job")
	default:
	}
	if e = r.CancelJob(context.Background(), job.ID); e != nil {
		t.Fatal(e)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not reach executor")
	}
	cancel()
	r.Wait()
	jobs, e := r.Jobs(context.Background())
	if e != nil || jobs[0].Status != "cancelled" {
		t.Fatalf("history=%v err=%v", jobs, e)
	}
}

func TestSmartOpsModelBillingUsesNativeFeesOnce(t *testing.T) {
	db := smartOpsDB(t)
	cfg := smartops.ModelBillingConfig{Enabled: true, Rules: []smartops.ModelBillingRule{{Model: "gpt-4o*", Multiplier: 3}, {Model: "gpt-4o-mini", Multiplier: 2}}}
	db.SetSmartOpsBilling(cfg, func() bool { return true })
	input := &UsageLogInput{Model: "gpt-4o-mini", EffectiveModel: "gpt-4o-mini", InputTokens: 1000000, OutputTokens: 1000, APIKeyID: 1}
	upstream := UsageLogBilledCost(input)
	frozen := db.SnapshotSmartOpsBilling(input)
	if UsageLogBilledCost(frozen) != upstream || UsageLogUserBilledCost(frozen) != upstream*2 {
		t.Fatalf("upstream=%v user=%v", UsageLogBilledCost(frozen), UsageLogUserBilledCost(frozen))
	}
	again := db.SnapshotSmartOpsBilling(frozen)
	if UsageLogUserBilledCost(again) != upstream*2 {
		t.Fatal("billing applied twice")
	}
	db.SetSmartOpsBilling(cfg, func() bool { return false })
	if UsageLogUserBilledCost(db.SnapshotSmartOpsBilling(input)) != upstream {
		t.Fatal("disabled billing active")
	}
	if input.billingSnapshot != nil {
		t.Fatal("caller record mutated")
	}
}

func TestSmartOpsRestartResumesOnlyUnpublishedSamples(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	job, e := db.CreatePelicanJob(ctx, smartops.PelicanJob{AccountID: 1, Model: "test", Samples: 2, Parallel: 1}, 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.ClaimPelicanJob(ctx, "lost-instance", time.Now()); e != nil {
		t.Fatal(e)
	}
	if e = db.SavePelicanResult(ctx, smartops.PelicanResult{LeaseOwner: "lost-instance", JobID: job.ID, AccountID: 1, Sample: 0, Status: "success", Output: "<html>saved before restart</html>"}); e != nil {
		t.Fatal(e)
	}
	if _, e = db.conn.ExecContext(ctx, `UPDATE smart_ops_test_jobs SET lease_until=0 WHERE id=$1`, job.ID); e != nil {
		t.Fatal(e)
	}
	parent, cancel := context.WithCancel(ctx)
	defer cancel()
	var calls atomic.Int32
	r := smartops.NewRuntime(func(context.Context, string) bool { return true })
	if e = r.Start(parent, db, func(context.Context, smartops.PelicanJob, int) (smartops.PelicanResult, error) {
		calls.Add(1)
		return smartops.PelicanResult{AccountID: 1, Output: "<html>new sample</html>"}, nil
	}); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		record, e := db.GetPelicanJob(ctx, job.ID)
		if e != nil {
			t.Fatal(e)
		}
		if record.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	r.Wait()
	record, e := db.GetPelicanJob(ctx, job.ID)
	if e != nil || record.Status != "completed" || calls.Load() != 1 || len(record.Results) != 2 || record.Results[0].Output != "<html>saved before restart</html>" {
		t.Fatalf("restart replayed published sample: record=%+v calls=%d err=%v", record, calls.Load(), e)
	}
}
func TestSmartOpsDisabledWorkerStopsInFlightAndLeavesQueue(t *testing.T) {
	db := smartOpsDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var on atomic.Bool
	on.Store(true)
	var calls atomic.Int32
	started := make(chan struct{})
	stopped := make(chan struct{})
	r := smartops.NewRuntime(func(context.Context, string) bool { return on.Load() })
	if e := r.Start(ctx, db, func(ctx context.Context, j smartops.PelicanJob, _ int) (smartops.PelicanResult, error) {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		close(stopped)
		return smartops.PelicanResult{AccountID: j.AccountID}, ctx.Err()
	}); e != nil {
		t.Fatal(e)
	}
	job, e := r.CreateJob(ctx, smartops.PelicanJob{AccountID: 1, Model: "test", Samples: 1, Parallel: 1})
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	on.Store(false)
	queued, e := db.CreatePelicanJob(ctx, smartops.PelicanJob{AccountID: 2, Model: "test", Samples: 1, Parallel: 1}, 0)
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("disabled worker did not cancel native invocation")
	}
	cancel()
	r.Wait()
	first, e := db.GetPelicanJob(context.Background(), job.ID)
	if e != nil {
		t.Fatal(e)
	}
	second, e := db.GetPelicanJob(context.Background(), queued.ID)
	if e != nil || first.Status != "cancelled" && first.Status != "interrupted" || second.Status != "queued" || calls.Load() != 1 {
		t.Fatalf("off behavior: first=%v queued=%v calls=%d err=%v", first.Status, second.Status, calls.Load(), e)
	}
}
func TestSmartOpsNativeQualityEvidenceInfluencesCachedScore(t *testing.T) {
	db := smartOpsDB(t)
	ctx := context.Background()
	if _, e := db.conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS account_quality_rounds(id INTEGER PRIMARY KEY,account_id BIGINT,created_at TIMESTAMP,record TEXT)`); e != nil {
		t.Fatal(e)
	}
	record := `{"passed_count":0,"total_count":2,"completed_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","outcome":"degraded"}`
	if _, e := db.conn.ExecContext(ctx, `INSERT INTO account_quality_rounds(id,account_id,created_at,record) VALUES(1,77,$1,$2)`, db.timeArg(time.Now()), record); e != nil {
		t.Fatal(e)
	}
	signals, e := db.ReadSmartOpsSignals(ctx, smartops.DefaultPriorityConfig())
	if e != nil || !signals[77].QualityKnown || signals[77].QualityPercent != 0 || signals[77].QualityObservedUnix == 0 {
		t.Fatalf("native quality observation unavailable: %+v %v", signals, e)
	}
}
