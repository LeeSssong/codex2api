package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const (
	TurnStateMissNone          = "none"
	TurnStateMissRebindGroup   = "rebind_group"
	TurnStateMissUnbindGroups  = "unbind_groups"
	TurnStateMissUnschedulable = "unschedulable"
	TurnStateRecoveredNone     = "none"
	TurnStateRecoveredRebind   = "rebind_group"
	TurnStateRecoveredRestore  = "restore_schedulable"
)

type TurnStateReuseSettings struct {
	Enabled                bool     `json:"enabled"`
	HarvestModel           string   `json:"harvest_model"`
	HarvestProxyURLs       []string `json:"harvest_proxy_urls"`
	HarvestUseProxyPool    bool     `json:"harvest_use_proxy_pool"`
	MissAction             string   `json:"miss_action"`
	MissTargetGroupID      *int64   `json:"miss_target_group_id,omitempty"`
	RecoveredAction        string   `json:"recovered_action"`
	RecoveredTargetGroupID *int64   `json:"recovered_target_group_id,omitempty"`
	InjectCompact          bool     `json:"inject_compact"`
}

func DefaultTurnStateReuseSettings() TurnStateReuseSettings {
	return TurnStateReuseSettings{HarvestModel: "gpt-6-astra", HarvestUseProxyPool: true, MissAction: TurnStateMissNone, RecoveredAction: TurnStateRecoveredNone, HarvestProxyURLs: []string{}}
}

func NormalizeTurnStateReuseSettings(in TurnStateReuseSettings) (TurnStateReuseSettings, error) {
	out := in
	out.HarvestModel = "gpt-6-astra"
	out.InjectCompact = false
	if out.HarvestProxyURLs == nil {
		out.HarvestProxyURLs = []string{}
	}
	for i := range out.HarvestProxyURLs {
		out.HarvestProxyURLs[i] = strings.TrimSpace(out.HarvestProxyURLs[i])
	}
	switch out.MissAction {
	case TurnStateMissNone, TurnStateMissUnbindGroups, TurnStateMissUnschedulable:
		out.MissTargetGroupID = nil
	case TurnStateMissRebindGroup:
		if out.MissTargetGroupID == nil || *out.MissTargetGroupID <= 0 {
			return out, errors.New("miss_target_group_id is required for rebind_group")
		}
	default:
		return out, errors.New("invalid miss_action")
	}
	switch out.RecoveredAction {
	case TurnStateRecoveredNone, TurnStateRecoveredRestore:
		out.RecoveredTargetGroupID = nil
	case TurnStateRecoveredRebind:
		if out.RecoveredTargetGroupID == nil || *out.RecoveredTargetGroupID <= 0 {
			return out, errors.New("recovered_target_group_id is required for rebind_group")
		}
	default:
		return out, errors.New("invalid recovered_action")
	}
	return out, nil
}

func (db *DB) GetTurnStateReuseSettings(ctx context.Context) (TurnStateReuseSettings, error) {
	result := DefaultTurnStateReuseSettings()
	var raw string
	if err := db.conn.QueryRowContext(ctx, `SELECT COALESCE(turn_state_reuse_config, '{}') FROM system_settings WHERE id = 1`).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, nil
		}
		return result, err
	}
	if strings.TrimSpace(raw) != "" && strings.TrimSpace(raw) != "{}" {
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			return DefaultTurnStateReuseSettings(), err
		}
	}
	return NormalizeTurnStateReuseSettings(result)
}

func (db *DB) UpdateTurnStateReuseSettings(ctx context.Context, settings TurnStateReuseSettings) error {
	normalized, err := NormalizeTurnStateReuseSettings(settings)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	if db.isSQLite() {
		_, err = db.conn.ExecContext(ctx, `INSERT INTO system_settings (id, turn_state_reuse_config) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET turn_state_reuse_config=excluded.turn_state_reuse_config`, string(raw))
	} else {
		_, err = db.conn.ExecContext(ctx, `INSERT INTO system_settings (id, turn_state_reuse_config) VALUES (1, $1) ON CONFLICT(id) DO UPDATE SET turn_state_reuse_config=EXCLUDED.turn_state_reuse_config`, string(raw))
	}
	return err
}
