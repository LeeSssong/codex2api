package database

import (
	"context"
	"github.com/codex2api/accountops"
	"testing"
	"time"
)

func TestBasispointsOpsGenerationCooldownAndSMTPIndependence(t *testing.T) {
	db, e := newAccountOpsTestDB(t)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	r := NewAccountOpsRepository(db)
	if e = NewAccountOpsSettings(db).Set(ctx, "module_enabled", "true"); e != nil {
		t.Fatal(e)
	}
	id, e := db.InsertAccount(ctx, "BPS ops", "synthetic-rt", "")
	if e != nil {
		t.Fatal(e)
	}
	row, _ := db.GetAccountByID(ctx, id)
	event := accountops.AccountOpsEvent{AccountID: id, CredentialGeneration: row.CredentialGeneration, Kind: "auto_403_disabled", Signal: "must not persist raw"}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	if e = r.SuppressDisabled(ctx, accountops.DefaultConfig()); e != nil {
		t.Fatal(e)
	}
	got, e := r.Claim(ctx)
	if e != nil || got == nil || got.Occurrences != 2 || got.Kind != "auto_403_disabled" || got.Signal != "auto_403_disabled" || got.CredentialGeneration != row.CredentialGeneration {
		t.Fatal(got, e)
	}
	if e = r.Complete(ctx, got, "sent", time.Hour); e != nil {
		t.Fatal(e)
	}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	if next, e := r.Claim(ctx); next != nil || e != nil {
		t.Fatal("cooldown ignored", next, e)
	}
	_, e = db.conn.ExecContext(ctx, "UPDATE accounts SET credential_generation=credential_generation+1 WHERE id=$1", id)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	items, e := r.List(ctx, 0, 100)
	if e != nil || len(items) != 1 || items[0].Occurrences != 3 {
		t.Fatal("stale recorded", items, e)
	}
	event.CredentialGeneration++
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	got, e = r.Claim(ctx)
	if e != nil || got == nil {
		t.Fatal(got, e)
	}
	_, e = db.conn.ExecContext(ctx, "UPDATE accounts SET credential_generation=credential_generation+1 WHERE id=$1", id)
	if e != nil {
		t.Fatal(e)
	}
	called := false
	sent, e := r.WithCurrentBasispointsEvent(ctx, got, func(context.Context) error { called = true; return nil })
	if e != nil || sent || called {
		t.Fatal("stale notification delivered", sent, called, e)
	}
}
func TestBasispointsOpsGlobalCleanupAndModuleGate(t *testing.T) {
	db, e := newAccountOpsTestDB(t)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	r := NewAccountOpsRepository(db)
	event := accountops.AccountOpsEvent{Kind: "image_cleanup_failed"}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	items, e := r.List(ctx, 0, 100)
	if e != nil || len(items) != 0 {
		t.Fatal("module gate ignored", items, e)
	}
	if e = NewAccountOpsSettings(db).Set(ctx, "module_enabled", "true"); e != nil {
		t.Fatal(e)
	}
	if e = r.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	claimed, e := r.Claim(ctx)
	if e != nil || claimed == nil || claimed.AccountID != 0 {
		t.Fatal(claimed, e)
	}
	called := false
	sent, e := r.WithCurrentBasispointsEvent(ctx, claimed, func(context.Context) error { called = true; return nil })
	if e != nil || !sent || !called {
		t.Fatal("global cleanup failed", sent, e)
	}
	if e = r.Record(ctx, accountops.AccountOpsEvent{Kind: "auto_403_disabled"}); e == nil {
		t.Fatal("zero account auth event accepted")
	}
}
