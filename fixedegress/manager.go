package fixedegress

import (
	"context"
	"fmt"
	"github.com/codex2api/database"
	"net/http"
	"time"
)

type PoolConfig struct {
	ID       string
	Enabled  bool
	Strategy string
}
type Resource struct {
	ID, PoolID, ProxyURL, ObservedExitIP, State, ProviderTaskID string
	Owned                                                       bool
}
type Manager struct {
	db     *database.DB
	client *http.Client
}

func NewManager(db *database.DB, client *http.Client) *Manager {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	m := &Manager{db: db, client: client}
	return m
}
func (m *Manager) EnablePool(ctx context.Context, p PoolConfig) error {
	if p.ID == "" {
		return fmt.Errorf("pool id required")
	}
	if p.Strategy != "strict" && p.Strategy != "stable" {
		return fmt.Errorf("invalid strategy")
	}
	return m.db.SaveFixedEgressPool(ctx, database.FixedEgressPool{ID: p.ID, Enabled: p.Enabled, Strategy: p.Strategy})
}
func (m *Manager) UpsertResource(ctx context.Context, r Resource) error {
	if _, e := ParseStaticProxy(r.ProxyURL); e != nil {
		return e
	}
	if r.ObservedExitIP == "" {
		return fmt.Errorf("verified exit ip required")
	}
	return m.db.SaveFixedEgressResource(ctx, database.FixedEgressResource{ID: r.ID, PoolID: r.PoolID, ProxyURL: r.ProxyURL, ObservedExitIP: r.ObservedExitIP, State: r.State, Owned: r.Owned, ProviderTaskID: r.ProviderTaskID})
}
func (m *Manager) Enroll(ctx context.Context, account int64, pool string) (database.FixedEgressBinding, error) {
	return m.db.EnrollAndBindFixedEgress(ctx, account, pool)
}
func (m *Manager) Resolve(ctx context.Context, account int64) (database.FixedEgressBinding, error) {
	return m.db.ResolveFixedEgress(ctx, account)
}
func (m *Manager) Rotate(ctx context.Context, account, generation int64, resource string) error {
	return m.db.RotateFixedEgress(ctx, account, generation, resource)
}
