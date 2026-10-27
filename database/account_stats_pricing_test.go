package database

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func statsFloat(v float64) *float64 { return &v }

func TestAccountStatsPricingPriorityAndIsolation(t *testing.T) {
	input := &UsageLogInput{Model: "gpt-5.4", EffectiveModel: "gpt-5.4", InputTokens: 1000, CachedTokens: 400, OutputTokens: 100, BillingServiceTier: "priority", ReasoningEffort: "high"}
	sale := UsageLogBilledCost(input)
	cfg := AccountStatsPricingConfig{RateMultiplier: statsFloat(3), ApplySalePricing: true, Rules: []AccountStatsPricingRule{{Models: []string{"gpt-*"}, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(2), Output: statsFloat(10), CacheRead: statsFloat(.2)}, ReasoningMultipliers: map[string]float64{"high": 2}}}}
	got := SnapshotAccountStatsCost(input, cfg)
	want := (.0006*2 + .0001*10 + .0004*.2) * 2
	if math.Abs(*got.AccountStatsCost-want) > 1e-10 || math.Abs(*got.AccountCost-want*3) > 1e-10 {
		t.Fatalf("snapshot=%+v want stats %v account %v", got, want, want*3)
	}
	if UsageLogUserBilledCost(got) != sale || UsageLogBilledCost(got) != sale {
		t.Fatal("account statistical rules changed customer billing")
	}
	if input.AccountCost != nil {
		t.Fatal("mutated caller")
	}
	cfg.Rules = nil
	got = SnapshotAccountStatsCost(input, cfg)
	if *got.AccountCost != sale*3 || got.AccountCostSource != "sale" {
		t.Fatalf("sale snapshot %+v", got)
	}
	cfg.RateMultiplier = statsFloat(0)
	got = SnapshotAccountStatsCost(input, cfg)
	if *got.AccountCost != 0 {
		t.Fatal("explicit zero multiplier lost")
	}
}

func TestAccountStatsImageTierIsFinalAndZeroIsValid(t *testing.T) {
	for _, price := range []float64{0, .05} {
		cfg := AccountStatsPricingConfig{RateMultiplier: statsFloat(7), Rules: []AccountStatsPricingRule{{Models: []string{"gpt-image-*"}, ImageTiers: map[string]float64{"2K": price}}}}
		got := SnapshotAccountStatsCost(&UsageLogInput{Model: "gpt-image-2.5-sunburst", ImageSize: "2048x1024", ImageCount: 2, InputTokens: 100}, cfg)
		if got.AccountCost == nil || *got.AccountCost != price*2 || *got.AccountStatsCost != price*2 {
			t.Fatalf("image tier snapshot %+v", got)
		}
	}
}

func TestAccountStatsSnapshotsFreezePricesAndOriginalInputs(t *testing.T) {
	input := &UsageLogInput{Model: "customer-alias", EffectiveModel: "gpt-5.4", InputTokens: 300000, CachedTokens: 100000, OutputTokens: 1000, ActualServiceTier: "priority"}
	cfg := AccountStatsPricingConfig{RateMultiplier: statsFloat(2)}
	got := SnapshotAccountStatsCost(input, cfg)
	original := *got.AccountCost
	SetModelPricingOverrides(map[string]ModelPricingOverride{"gpt-5.4": {Input: 99, Output: 99}})
	defer SetModelPricingOverrides(nil)
	again := SnapshotAccountStatsCost(got, cfg)
	if *again.AccountCost != original {
		t.Fatal("recomputed frozen fee")
	}
	var frozen map[string]any
	if err := json.Unmarshal([]byte(got.AccountPricingSnapshot), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen["model"] != "gpt-5.4" || frozen["input_tokens"] != float64(300000) || frozen["cached_tokens"] != float64(100000) || frozen["service_tier"] != "priority" {
		t.Fatalf("original inputs not preserved: %s", got.AccountPricingSnapshot)
	}
}

func TestAccountStatsPersistence(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "cost.db")
			if driver == "postgres" {
				dsn = os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("requires isolated CODEX2API_TEST_POSTGRES_DSN")
				}
			}
			db, err := New(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ctx := context.Background()
			input := SnapshotAccountStatsCost(&UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, OutputTokens: 100, StatusCode: 200}, AccountStatsPricingConfig{RateMultiplier: statsFloat(0)})
			if err := db.InsertUsageLog(ctx, input); err != nil {
				t.Fatal(err)
			}
			db.FlushUsageLogs()
			var cost, stats, standard, mult float64
			var source, snapshot string
			if err := db.conn.QueryRowContext(ctx, `SELECT account_cost,account_stats_cost,standard_cost,account_rate_multiplier,account_cost_source,account_pricing_snapshot FROM usage_logs ORDER BY id DESC LIMIT 1`).Scan(&cost, &stats, &standard, &mult, &source, &snapshot); err != nil {
				t.Fatal(err)
			}
			if cost != 0 || mult != 0 || stats <= 0 || standard <= 0 || source != "upstream_default" || snapshot == "" {
				t.Fatalf("cost snapshot %v/%v/%v/%v/%s", cost, stats, standard, mult, source)
			}
			var resolved float64
			if err := db.conn.QueryRowContext(ctx, `SELECT `+AccountCostSQL("")+` FROM usage_logs ORDER BY id DESC LIMIT 1`).Scan(&resolved); err != nil || resolved != 0 {
				t.Fatalf("resolve explicit zero %v %v", resolved, err)
			}
		})
	}
}

// Values traced to pinned Sub2API a6d32ede by source-audit.md. Customer sale
// overrides deliberately diverge so accidental reuse of native billing fails.
func TestAccountStatsSourceConformance(t *testing.T) {
	SetModelPricingOverrides(map[string]ModelPricingOverride{"gpt-5.4": {Input: 99, Output: 99}})
	defer SetModelPricingOverrides(nil)
	prices := AccountStatsPrices{Input: statsFloat(2.5), Output: statsFloat(15), CacheRead: statsFloat(.25), CacheWrite: statsFloat(2.5)}
	catalog := AccountStatsPricingRule{Models: []string{"gpt-5.4"}, AccountStatsPrices: prices, LongContextThreshold: 272000, LongInputMultiplier: 2, LongOutputMultiplier: 1.5}
	cases := []struct {
		name string
		in   UsageLogInput
		cfg  AccountStatsPricingConfig
		want float64
	}{
		{name: "ordinary cached standard", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100}, want: .00355},
		{name: "priority", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100, ActualServiceTier: "priority"}, want: .0071},
		{name: "flex", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100, ActualServiceTier: "flex"}, want: .001775},
		{name: "catalog long", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 301000, CachedTokens: 300000, OutputTokens: 1000}, cfg: AccountStatsPricingConfig{UpstreamPrices: []AccountStatsPricingRule{catalog}}, want: .1775},
		{name: "catalog long write", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 311000, CachedTokens: 300000, CacheWrite5mTokens: 10000, OutputTokens: 1000}, cfg: AccountStatsPricingConfig{UpstreamPrices: []AccountStatsPricingRule{catalog}}, want: .2275},
		{name: "strict catalog threshold", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 272000, OutputTokens: 1000}, cfg: AccountStatsPricingConfig{UpstreamPrices: []AccountStatsPricingRule{catalog}}, want: .695},
		{name: "fallback has no invented long", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 301000, CachedTokens: 300000, OutputTokens: 1000}, want: .0925},
		{name: "custom original categories", in: UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, CacheWrite5mTokens: 100, OutputTokens: 100}, cfg: AccountStatsPricingConfig{RateMultiplier: statsFloat(2), Rules: []AccountStatsPricingRule{{Models: []string{"gpt-5.4"}, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(1), Output: statsFloat(2), CacheRead: statsFloat(.5), CacheWrite: statsFloat(3)}}}}, want: .0026},
		{name: "source spark price", in: UsageLogInput{Model: "gpt-5.3-codex-spark", InputTokens: 1000, OutputTokens: 100}, want: .0027},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SnapshotAccountStatsCost(&tc.in, tc.cfg)
			if math.Abs(*got.AccountCost-tc.want) > 1e-10 {
				t.Fatalf("account %v want %v source %s snapshot%s", *got.AccountCost, tc.want, got.AccountCostSource, got.AccountPricingSnapshot)
			}
		})
	}
}

func TestAccountStatsOriginalEvidenceDoesNotAlterUserCost(t *testing.T) {
	input := &UsageLogInput{Model: "gpt-5.4", InputTokens: 9999, OutputTokens: 9999, AccountUsage: &AccountUsageEvidence{InputTokens: 1000, CachedTokens: 200, OutputTokens: 100}}
	user := UsageLogUserBilledCost(input)
	got := SnapshotAccountStatsCost(input, AccountStatsPricingConfig{})
	if math.Abs(*got.AccountCost-.00355) > 1e-10 || UsageLogUserBilledCost(got) != user {
		t.Fatalf("account %+v, user %v want %v", got, UsageLogUserBilledCost(got), user)
	}
}

func TestAccountStatsCustomIntervalAndMatching(t *testing.T) {
	upper := 1100
	cfg := AccountStatsPricingConfig{Rules: []AccountStatsPricingRule{{Pricing: []AccountStatsPricingRule{
		{Models: []string{"gpt-*"}, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(99)}},
		{Models: []string{"GPT-5.4"}, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(1)}, Intervals: []AccountStatsPricingInterval{{MinTokens: 0, MaxTokens: &upper, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(2), Output: statsFloat(3), CacheRead: statsFloat(1)}}}},
	}}}}
	// source exact match precedes wildcard within the account's pricing list;
	// interval includes cached input and output, with (min,max] boundaries.
	got := SnapshotAccountStatsCost(&UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100}, cfg)
	if math.Abs(*got.AccountCost-.0021) > 1e-10 {
		t.Fatalf("interval %v", *got.AccountCost)
	}
	got = SnapshotAccountStatsCost(&UsageLogInput{Model: "gpt-5.4", InputTokens: 1001, CachedTokens: 200, OutputTokens: 100}, cfg)
	if math.Abs(*got.AccountCost-.000801) > 1e-10 {
		t.Fatalf("outside interval %v", *got.AccountCost)
	}
}

func TestAccountStatsLegacyFallbackAndFrozenRead(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	_, err = db.conn.ExecContext(ctx, `INSERT INTO usage_logs(model,status_code,account_billed) VALUES('gpt-5.4',200,7)`)
	if err != nil {
		t.Fatal(err)
	}
	var cost float64
	if err := db.conn.QueryRowContext(ctx, `SELECT `+AccountCostSQL("u")+` FROM usage_logs u ORDER BY id DESC LIMIT 1`).Scan(&cost); err != nil || cost != 7 {
		t.Fatalf("legacy %v %v", cost, err)
	}
	input := SnapshotAccountStatsCost(&UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, OutputTokens: 100, StatusCode: 200}, AccountStatsPricingConfig{})
	if err := db.InsertUsageLog(ctx, input); err != nil {
		t.Fatal(err)
	}
	db.FlushUsageLogs()
	native := *input.StandardCost
	SetModelPricingOverrides(map[string]ModelPricingOverride{"gpt-5.4": {Input: 99, Output: 99}})
	defer SetModelPricingOverrides(nil)
	logs, err := db.ListRecentUsageLogs(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 || logs[0].AccountCost == nil || *logs[0].AccountCost != *input.AccountCost || logs[0].TotalCost != native {
		t.Fatalf("frozen read %+v want %v", logs, native)
	}
	if logs[0].InputPrice != 2.5 {
		t.Fatalf("display price re-evaluated: %+v", logs[0])
	}
}

func TestAccountStatsSourceFirstRuleBeforeSpecificModel(t *testing.T) {
	cfg := AccountStatsPricingConfig{Rules: []AccountStatsPricingRule{
		{AccountIDs: []int64{1}, Pricing: []AccountStatsPricingRule{{Models: []string{"gpt-*"}, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(2)}}}},
		{AccountIDs: []int64{1}, Pricing: []AccountStatsPricingRule{{Models: []string{"gpt-5.4"}, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(99)}}}},
	}}
	got := SnapshotAccountStatsCost(&UsageLogInput{AccountID: 1, Model: "gpt-5.4", InputTokens: 1000}, cfg)
	if *got.AccountCost != .002 {
		t.Fatalf("later exact shadowed earlier source rule: %v", *got.AccountCost)
	}
	cfg.Rules[0].AccountIDs = []int64{2}
	cfg.Rules[0].GroupIDs = []int64{8}
	got = SnapshotAccountStatsCost(&UsageLogInput{AccountID: 1, Model: "gpt-5.4", InputTokens: 1000}, cfg)
	if *got.AccountCost != .099 {
		t.Fatalf("group selector matched without configured source group: %v", *got.AccountCost)
	}
	group := int64(8)
	cfg.SourceGroupID = &group
	got = SnapshotAccountStatsCost(&UsageLogInput{AccountID: 1, Model: "gpt-5.4", InputTokens: 1000}, cfg)
	if *got.AccountCost != .002 {
		t.Fatalf("group OR account selector %v", *got.AccountCost)
	}
}

func TestAccountStatsSourceBuiltinLongAndUnknownModels(t *testing.T) {
	for _, tc := range []struct {
		model  string
		input  int
		want   float64
		source string
	}{
		{"gpt-6-sol", 301000, .001*4 + .300*.4 + .001*15, "upstream_default"},
		{"gpt-6.1-sol", 301000, .001*2 + .300*.1 + .001*10, "upstream_default"},
		{"gpt-6-future", 1000, 0, "fallback"},
	} {
		in := &UsageLogInput{Model: tc.model, InputTokens: tc.input, CachedTokens: 300000, OutputTokens: 1000}
		if tc.model == "gpt-6-future" {
			in.CachedTokens = 0
			tc.want = UsageLogBilledCost(in)
		}
		got := SnapshotAccountStatsCost(in, AccountStatsPricingConfig{})
		if math.Abs(*got.AccountCost-tc.want) > 1e-10 || got.AccountCostSource != tc.source {
			t.Fatalf("%s:cost%v source%s want%v/%s", tc.model, *got.AccountCost, got.AccountCostSource, tc.want, tc.source)
		}
	}
}

func TestAccountStatsObservedZeroDoesNotChargeEstimatedFallback(t *testing.T) {
	for _, sale := range []bool{false, true} {
		input := &UsageLogInput{Model: "unpriced-native-model", InputTokens: 1200, OutputTokens: 800, AccountUsage: &AccountUsageEvidence{}}
		user := UsageLogUserBilledCost(input)
		got := SnapshotAccountStatsCost(input, AccountStatsPricingConfig{ApplySalePricing: sale})
		if *got.AccountCost != 0 || *got.StandardCost != 0 || UsageLogUserBilledCost(got) != user {
			t.Fatalf("observed zero leaked estimates: %+v", got)
		}
	}
}

func TestAccountStatsOnlyDoesNotChangeNativeBilling(t *testing.T) {
	in := &UsageLogInput{AccountStatsOnly: true, Model: "gpt-5.4", InputTokens: 1000, OutputTokens: 100, AccountUsage: &AccountUsageEvidence{InputTokens: 1000, OutputTokens: 100}}
	if UsageLogBilledCost(in) != 0 || UsageLogUserBilledCost(in) != 0 {
		t.Fatal("statistics only partial attempt charged native customer")
	}
	got := SnapshotAccountStatsCost(in, AccountStatsPricingConfig{})
	if math.Abs(*got.AccountCost-.004) > 1e-10 || *got.StandardCost != .004 {
		t.Fatalf("lost source observed partial charge %+v", got)
	}
}

func TestAccountStatsApplySaleUsesConfiguredImageUnitPrice(t *testing.T) {
	SetModelPricingOverrides(map[string]ModelPricingOverride{"gpt-image-2.5-sunburst": {UserBillingMode: UserBillingModePerImage, ImageUnitPrice: .05}})
	defer SetModelPricingOverrides(nil)
	in := SnapshotUsageLogBilling(&UsageLogInput{Model: "gpt-image-2.5-sunburst", ImageCount: 2, InputTokens: 1000, OutputTokens: 100, StatusCode: 200})
	got := SnapshotAccountStatsCost(in, AccountStatsPricingConfig{ApplySalePricing: true, RateMultiplier: statsFloat(3)})
	if *got.StandardCost != .1 || math.Abs(*got.AccountCost-.3) > 1e-10 {
		t.Fatalf("sale ignored configured unit price %+v", got)
	}
	if UsageLogUserBilledCost(got) != .1 {
		t.Fatal("changed native image charge")
	}
}

func TestAccountStatsInconsistentOriginalTotalsUseExclusiveSourceCategories(t *testing.T) {
	upper := 150
	cfg := AccountStatsPricingConfig{Rules: []AccountStatsPricingRule{{Models: []string{"gpt-5.4"}, AccountStatsPrices: AccountStatsPrices{CacheRead: statsFloat(1)}, Intervals: []AccountStatsPricingInterval{{MinTokens: 0, MaxTokens: &upper, AccountStatsPrices: AccountStatsPrices{CacheRead: statsFloat(99)}}}}}}
	got := SnapshotAccountStatsCost(&UsageLogInput{Model: "gpt-5.4", InputTokens: 100, CachedTokens: 200}, cfg)
	if *got.AccountCost != .0002 {
		t.Fatalf("interval used inconsistent total instead of source exclusive categories: %v", *got.AccountCost)
	}
}

func TestAccountStatsEvidenceTierOverridesOnlyAccountPricing(t *testing.T) {
	tier := "priority"
	in := &UsageLogInput{Model: "gpt-5.4", InputTokens: 1000, CachedTokens: 200, OutputTokens: 100, ActualServiceTier: "flex", AccountUsage: &AccountUsageEvidence{InputTokens: 1000, CachedTokens: 200, OutputTokens: 100, ServiceTier: &tier}}
	native := UsageLogUserBilledCost(in)
	got := SnapshotAccountStatsCost(in, AccountStatsPricingConfig{})
	if math.Abs(*got.AccountCost-.0071) > 1e-10 || UsageLogUserBilledCost(got) != native || got.ActualServiceTier != "flex" {
		t.Fatalf("tier isolation %+v", got)
	}
}

func TestAccountStatsUnknownVersusKnownZeroPerRequest(t *testing.T) {
	cfg := AccountStatsPricingConfig{Rules: []AccountStatsPricingRule{{Models: []string{"gpt-5.4"}, PerRequest: statsFloat(.3)}}}
	for _, known := range []bool{false, true} {
		in := &UsageLogInput{Model: "gpt-5.4", OutputTokens: 10, AccountUsage: &AccountUsageEvidence{UsageKnown: &known}}
		got := SnapshotAccountStatsCost(in, cfg)
		want := 0.0
		if known {
			want = .3
		}
		if *got.AccountCost != want {
			t.Fatalf("known%v got%v want%v", known, *got.AccountCost, want)
		}
	}
}

func TestAccountStatsCatalogAndCustomImageOutputBuckets(t *testing.T) {
	price := AccountStatsPricingRule{Models: []string{"image-model"}, AccountStatsPrices: AccountStatsPrices{Input: statsFloat(2), Output: statsFloat(10), ImageOutput: statsFloat(30)}}
	in := &UsageLogInput{Model: "image-model", InputTokens: 100, OutputTokens: 100, ImageOutputTokens: 20}
	catalog := SnapshotAccountStatsCost(in, AccountStatsPricingConfig{UpstreamPrices: []AccountStatsPricingRule{price}})
	custom := SnapshotAccountStatsCost(in, AccountStatsPricingConfig{Rules: []AccountStatsPricingRule{price}})
	if math.Abs(*catalog.AccountCost-.0016) > 1e-10 || math.Abs(*custom.AccountCost-.0018) > 1e-10 {
		t.Fatalf("catalog%v custom%v", *catalog.AccountCost, *custom.AccountCost)
	}
}
