package admin

import (
	"context"
	"encoding/json"
	"github.com/codex2api/accountops"
	"github.com/codex2api/database"
	"github.com/codex2api/tokenguard"
)

type basispointsBarkClient interface {
	SendNotification(context.Context, tokenguard.Config, string, string, bool) error
}

func (h *Handler) basispointsOpsNotifier(client basispointsBarkClient) func(context.Context, *accountops.AccountOpsEvent) (bool, error) {
	if client == nil {
		client = tokenguard.NewClient(nil)
	}
	return func(ctx context.Context, event *accountops.AccountOpsEvent) (bool, error) {
		raw, _, err := h.db.TokenGuardConfig(ctx)
		if err != nil {
			return false, err
		}
		cfg := tokenguard.DefaultConfig()
		if err = json.Unmarshal([]byte(raw), &cfg); err != nil {
			return false, err
		}
		if !cfg.NotifyOnFail || cfg.BarkKey == "" {
			return false, nil
		}
		title, body := accountops.BasispointsNotification(*event)
		return database.NewAccountOpsRepository(h.db).WithCurrentBasispointsEvent(ctx, event, func(send context.Context) error { return client.SendNotification(send, cfg, title, body, true) })
	}
}
