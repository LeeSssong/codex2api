package admin

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/plugins"
	"github.com/codex2api/smartops"
	"net/http"
	"testing"
	"time"
)

func TestSmartOpsNativeOAuthDefaultsPersist(t *testing.T) {
	db := newTestAdminDB(t)
	ctx := context.Background()
	group, err := db.CreateAccountGroup(ctx, "automatic", "", "#246246", 0, 0, sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := smartops.DefaultOAuthAutoConfig()
	cfg.Enabled = true
	cfg.GroupIDs = []int64{group}
	cfg.UpgradeGroupIDs = []int64{group}
	cfg.UpgradeEnabled = true
	cfg.SuccessesPerStep = 2
	cfg.MaxConcurrency = 4
	if err = db.SaveOAuthAutoConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db, pluginRegistry: plugins.NewRegistry(nil)}
	id, err := h.insertSmartOpsOAuthAccount(ctx, "native", map[string]interface{}{"access_token": "synthetic"}, "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !row.BaseConcurrencyOverride.Valid || row.BaseConcurrencyOverride.Int64 != 3 {
		t.Fatalf("defaults not persisted: %+v", row.BaseConcurrencyOverride)
	}
	groups, err := db.GetAccountGroupIDs(ctx, id)
	if err != nil || len(groups) != 1 || groups[0] != group {
		t.Fatalf("groups=%v err=%v", groups, err)
	}
}

func TestSmartOpsNativeDefaultsWithSingleSQLiteConnection(t *testing.T) {
	db, e := database.New("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	group, e := db.CreateAccountGroup(ctx, "initial", "", "#334455", 0, 0, sql.NullInt64{})
	if e != nil {
		t.Fatal(e)
	}
	cfg := smartops.DefaultOAuthAutoConfig()
	cfg.Enabled = true
	cfg.GroupIDs = []int64{group}
	if e = db.SaveOAuthAutoConfig(ctx, cfg); e != nil {
		t.Fatal(e)
	}
	h := &Handler{db: db, store: auth.NewStore(db, nil, nil), pluginRegistry: plugins.NewRegistry(database.NewPluginStore(db))}
	if e = h.InitSmartOps(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() { cancel(); h.WaitSmartOps() }()
	bounded, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	id, e := db.InsertAccountWithCredentials(bounded, "real native hook", map[string]interface{}{"access_token": "synthetic-token"}, "")
	if e != nil {
		t.Fatalf("native publication blocked or failed: %v", e)
	}
	row, e := db.GetAccountByID(ctx, id)
	if e != nil || !row.BaseConcurrencyOverride.Valid || row.BaseConcurrencyOverride.Int64 != int64(cfg.Concurrency) {
		t.Fatalf("native defaults not consumed: %+v %v", row, e)
	}
}

func TestSmartOpsPelicanInvokesNativeExecutorAndBilling(t *testing.T) {
	h, _, _ := newCodexDiagnosticsTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"<!doctype html><html>native</html>\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-4o-mini\",\"usage\":{\"input_tokens\":24,\"output_tokens\":88}}}\n\n")
	})
	h.pluginRegistry = plugins.NewRegistry(nil)
	h.db = newTestAdminDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if e := h.InitSmartOps(ctx); e != nil {
		t.Fatal(e)
	}
	job, e := h.smartOps.CreateJob(ctx, smartops.PelicanJob{AccountID: 42, Model: "gpt-4o-mini", Samples: 1, Parallel: 1})
	if e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	var results []smartops.JobRecord
	for time.Now().Before(deadline) {
		results, e = h.smartOps.Jobs(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if len(results) > 0 && results[0].Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	h.WaitSmartOps()
	if detail, e := h.smartOps.GetJob(context.Background(), job.ID); e == nil {
		results = []smartops.JobRecord{detail}
	} else {
		t.Fatal(e)
	}
	if len(results) == 0 || results[0].ID != job.ID || results[0].Status != "completed" || len(results[0].Results) != 1 || results[0].Results[0].Output != "<!doctype html><html>native</html>" {
		t.Fatalf("native execution results=%+v", results)
	}
	h.db.FlushUsageLogs()
	signals, e := h.db.ReadSmartOpsSignals(context.Background(), smartops.DefaultPriorityConfig())
	if e != nil || signals[42].Samples != 1 {
		t.Fatalf("native usage missing: %v %v", signals, e)
	}
}
