package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/codex2api/database"
)

const accountStatsPricingCredentialKey = "account_stats_pricing"

func accountStatsPricingFromRow(row *database.AccountRow) database.AccountStatsPricingConfig {
	var cfg database.AccountStatsPricingConfig
	if row == nil {
		return cfg
	}
	value := row.Credentials[accountStatsPricingCredentialKey]
	raw, e := json.Marshal(value)
	if text, ok := value.(string); ok {
		raw = []byte(text)
	}
	if e != nil || json.Unmarshal(raw, &cfg) != nil || database.ValidateAccountStatsPricing(cfg) != nil {
		return database.AccountStatsPricingConfig{}
	}
	return cfg
}
func (a *Account) AccountStatsPricing() database.AccountStatsPricingConfig {
	if a == nil {
		return database.AccountStatsPricingConfig{}
	}
	a.mu.RLock()
	raw, _ := json.Marshal(a.accountStatsPricing)
	a.mu.RUnlock()
	var cfg database.AccountStatsPricingConfig
	_ = json.Unmarshal(raw, &cfg)
	return cfg
}
func (s *Store) SetAccountStatsPricing(ctx context.Context, id int64, cfg database.AccountStatsPricingConfig) error {
	if e := database.ValidateAccountStatsPricing(cfg); e != nil {
		return e
	}
	acc := s.FindByID(id)
	if acc == nil {
		return fmt.Errorf("account not loaded")
	}
	raw, e := json.Marshal(cfg)
	if e != nil {
		return e
	}
	if s.db == nil {
		return fmt.Errorf("database unavailable")
	}
	if e := s.db.UpdateCredentials(ctx, id, map[string]any{accountStatsPricingCredentialKey: string(raw)}); e != nil {
		return e
	}
	// Decode a private copy so callers cannot alter live pricing through a map.
	var frozen database.AccountStatsPricingConfig
	_ = json.Unmarshal(raw, &frozen)
	acc.mu.Lock()
	acc.accountStatsPricing = frozen
	acc.mu.Unlock()
	return nil
}
