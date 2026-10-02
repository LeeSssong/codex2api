package admin

import (
	"context"
	"github.com/codex2api/accountops"
	"github.com/codex2api/database"
	"github.com/codex2api/smartops"
	"path/filepath"
	"testing"
)

func TestQualityRuleTemplateDefaultsAndNativeModelValidation(t *testing.T) {
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "quality-template.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = db.EnsureSmartOpsSchema(ctx); err != nil {
		t.Fatal(err)
	}
	config := smartops.DefaultOAuthAutoConfig()
	config.BPS.OmitUnsupportedTools = true
	config.BPS.IgnoreEncryptedContent = false
	config.BPS.AutoDisableOn403 = false
	config.BPS.AllModels = false
	config.BPS.Models = []string{"gpt-6-astra"}
	if err = db.SaveOAuthAutoConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	p := accountops.Plan{Action: "enable_bps"}
	if err = h.prepareQualityBPSPolicy(ctx, &p); err != nil {
		t.Fatal(err)
	}
	if p.BPS == nil || !p.BPS.OmitUnsupportedTools || p.BPS.IgnoreEncryptedContent || p.BPS.AutoDisableOn403 || p.BPS.FailureThreshold != 1 {
		t.Fatalf("template not preserved: %+v", p.BPS)
	}
	explicit := accountops.Plan{Action: "enable_bps", BPS: &accountops.QualityBPSPolicy{FailureThreshold: 2, PassThreshold: 3, AllModels: true, AutoDisableOn403: true}}
	if err = h.prepareQualityBPSPolicy(ctx, &explicit); err != nil {
		t.Fatal(err)
	}
	if !explicit.BPS.AutoDisableOn403 || explicit.BPS.FailureThreshold != 2 || explicit.BPS.PassThreshold != 3 {
		t.Fatal("explicit rule overwritten")
	}
	invalid := accountops.Plan{Action: "enable_bps", BPS: &accountops.QualityBPSPolicy{FailureThreshold: 1, Models: []string{"unsupported-test-model"}}}
	if err = h.prepareQualityBPSPolicy(ctx, &invalid); err == nil {
		t.Fatal("unsupported native model accepted")
	}
}
