package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTurnStateReuseSettingsDefaultsWhenSystemSettingsRowMissing(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "turn-state-defaults.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM system_settings`); err != nil {
		t.Fatalf("delete system_settings: %v", err)
	}

	got, err := db.GetTurnStateReuseSettings(ctx)
	if err != nil {
		t.Fatalf("GetTurnStateReuseSettings: %v", err)
	}
	want := DefaultTurnStateReuseSettings()
	if got.Enabled != want.Enabled || got.HarvestModel != want.HarvestModel || got.HarvestUseProxyPool != want.HarvestUseProxyPool || got.MissAction != want.MissAction || got.RecoveredAction != want.RecoveredAction || got.InjectCompact {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestTurnStateReuseSettingsRoundTripAndNormalize(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "turn-state-roundtrip.db"))
	if err != nil {
		t.Fatalf("New(sqlite): %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	missGroupID := int64(12)
	settings := TurnStateReuseSettings{
		Enabled:                true,
		HarvestModel:           "ignored-model",
		HarvestProxyURLs:       []string{" http://proxy.example:8080 "},
		HarvestUseProxyPool:    false,
		MissAction:             TurnStateMissRebindGroup,
		MissTargetGroupID:      &missGroupID,
		RecoveredAction:        TurnStateRecoveredRestore,
		RecoveredTargetGroupID: &missGroupID,
		InjectCompact:          true,
	}
	if err := db.UpdateTurnStateReuseSettings(ctx, settings); err != nil {
		t.Fatalf("UpdateTurnStateReuseSettings: %v", err)
	}

	got, err := db.GetTurnStateReuseSettings(ctx)
	if err != nil {
		t.Fatalf("GetTurnStateReuseSettings: %v", err)
	}
	if !got.Enabled || got.HarvestModel != "gpt-6-astra" || got.InjectCompact || got.HarvestUseProxyPool {
		t.Fatalf("normalized settings = %+v", got)
	}
	if len(got.HarvestProxyURLs) != 1 || got.HarvestProxyURLs[0] != "http://proxy.example:8080" {
		t.Fatalf("harvest_proxy_urls = %#v", got.HarvestProxyURLs)
	}
	if got.MissTargetGroupID == nil || *got.MissTargetGroupID != missGroupID {
		t.Fatalf("miss_target_group_id = %v", got.MissTargetGroupID)
	}
	if got.RecoveredTargetGroupID != nil {
		t.Fatalf("recovered_target_group_id = %v, want nil", got.RecoveredTargetGroupID)
	}
}

func TestTurnStateReuseSettingsRequireTargetsForRebind(t *testing.T) {
	for _, settings := range []TurnStateReuseSettings{
		{MissAction: TurnStateMissRebindGroup, RecoveredAction: TurnStateRecoveredNone},
		{MissAction: TurnStateMissNone, RecoveredAction: TurnStateRecoveredRebind},
	} {
		if _, err := NormalizeTurnStateReuseSettings(settings); err == nil {
			t.Fatalf("NormalizeTurnStateReuseSettings(%+v) succeeded, want error", settings)
		}
	}
}
