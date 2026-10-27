package database

import (
	"context"
	"math"
	"testing"
)

func TestCodexTextUserChargeMatchesAccountCostWithoutSeparateTariff(t *testing.T) {
	previous := currentModelPricingOverrides()
	SetModelPricingOverrides(map[string]ModelPricingOverride{"gpt-5.4": {Input: 99, Output: 99, CachedInput: 99}})
	defer SetModelPricingOverrides(previous)
	input := &UsageLogInput{Channel: UpstreamChannelCodex, Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100, BillingServiceTier: "default"}
	snapshot := SnapshotAccountStatsCost(input, AccountStatsPricingConfig{})
	if snapshot.AccountCost == nil {
		t.Fatal("missing cost")
	}
	for _, got := range []float64{UsageLogBilledCost(snapshot), UsageLogUserBilledCost(snapshot)} {
		if math.Abs(got-*snapshot.AccountCost) > 1e-12 {
			t.Fatalf("account=%v billed=%v user=%v", *snapshot.AccountCost, UsageLogBilledCost(snapshot), UsageLogUserBilledCost(snapshot))
		}
	}
	// Native non-Codex providers and unsnapshotted historical events retain their
	// explicit pricing path; only actual Codex text cost snapshots unify charges.
	other := *snapshot
	other.Channel = UpstreamChannelClaude
	if UsageLogBilledCost(&other) != UsageLogBilledCost(input) {
		t.Fatal("unrelated provider pricing changed")
	}
}

func TestCodexUnifiedCostPersistsSameUserChargeAndQuota(t *testing.T) {
	db := newGrokStateTestDB(t)
	ctx := context.Background()
	keyID, err := db.InsertAPIKey(ctx, "unified-cost", "fixture-unified-cost-key")
	if err != nil {
		t.Fatal(err)
	}
	admitted := true
	tokens := int64(1100)
	input := &UsageLogInput{Channel: UpstreamChannelCodex, AccountID: 79, APIKeyID: keyID, Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100, TotalTokens: 1100, StatusCode: 200}
	input.QuotaUsageAdmitted = &admitted
	input.QuotaTokens = &tokens
	if err = db.InsertUsageLog(ctx, input); err != nil {
		t.Fatal(err)
	}
	db.FlushUsageLogs()
	var account, user, ledger float64
	if err = db.conn.QueryRowContext(ctx, `SELECT account_billed,user_billed FROM usage_logs WHERE api_key_id=$1`, keyID).Scan(&account, &user); err != nil {
		t.Fatal(err)
	}
	if err = db.conn.QueryRowContext(ctx, `SELECT account_cost FROM account_usage_cost_events WHERE account_id=79`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	key, err := db.GetAPIKeyByID(ctx, keyID)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []float64{account, user, ledger, key.QuotaUsed, key.TotalUsed} {
		if math.Abs(value-.00355) > 1e-12 {
			t.Fatalf("account=%v user=%v ledger=%v key=%+v", account, user, ledger, key)
		}
	}
}
