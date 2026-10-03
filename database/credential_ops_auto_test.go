package database

import (
	"context"
	"testing"
)

func TestCredentialOpsAutomaticallyMonitorsNativeOAuthImports(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertAccountWithCredentials(ctx, "auto@example.com", map[string]any{"email": "auto@example.com", "access_token": "fixture"}, "")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListCredentialOpsMonitors(ctx)
	if err != nil || len(rows) != 1 || rows[0].AccountID != id || !rows[0].Enabled {
		t.Fatalf("import did not enroll: %#v %v", rows, err)
	}
	m, err := db.ClaimCredentialOpsMonitor(ctx, 0, "scheduler")
	if err != nil || m == nil || m.AccountID != id {
		t.Fatalf("scheduler cannot claim imported account: %#v %v", m, err)
	}
}

func TestCredentialGlobalRulesApplyToExistingAndFutureAccounts(t *testing.T) {
	db, err := New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.EnsureCredentialOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	importAccount := func(email string) int64 {
		id, e := db.InsertAccountWithCredentials(ctx, email, map[string]any{"email": email, "access_token": "fixture"}, "")
		if e != nil {
			t.Fatal(e)
		}
		return id
	}
	first := importAccount("first@example.com")
	before, _ := db.ListCredentialOpsMonitors(ctx)
	c := CredentialOpsGlobalRules{RetrySeconds: 60, IntervalSeconds: 900, FailureThreshold: 3, CooldownSeconds: 3600}
	if err = db.SaveCredentialOpsGlobalRules(ctx, c); err != nil {
		t.Fatal(err)
	}
	second := importAccount("second@example.com")
	rows, err := db.ListCredentialOpsMonitors(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %v", rows, err)
	}
	for _, r := range rows {
		if !r.Enabled || !r.AutoRelogin || r.IntervalSeconds != 900 || r.FailureThreshold != 3 || r.CooldownSeconds != 3600 {
			t.Fatalf("rules not applied: %#v", r)
		}
	}
	if rows[0].AccountID != first || rows[1].AccountID != second || len(before) != 1 {
		t.Fatal("unexpected accounts")
	}
	saved, err := db.CredentialOpsGlobalRules(ctx)
	if err != nil || saved != c {
		t.Fatalf("saved rules: %#v %v", saved, err)
	}
	c.IntervalSeconds = 0
	if db.SaveCredentialOpsGlobalRules(ctx, c) == nil {
		t.Fatal("invalid rules accepted")
	}
	saved, _ = db.CredentialOpsGlobalRules(ctx)
	if saved.IntervalSeconds != 900 {
		t.Fatal("invalid save changed global rules")
	}
}
