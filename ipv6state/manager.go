package ipv6state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/statepool"
)

type ExecuteFunc func(context.Context, *auth.Account, string, Route) (*http.Response, error)
type RouteFunc func(context.Context, Config, int64) (Route, error)
type FailureFunc func(*auth.Account, string, *http.Response)

type Manager struct {
	db       *database.DB
	store    *auth.Store
	execute  ExecuteFunc
	route    RouteFunc
	failure  FailureFunc
	now      func() time.Time
	localIPs func() ([]string, error)
	mu       sync.RWMutex
	config   Config
	entries  map[string]Entry
	revision uint64
	cursor   int
	nextAt   int64
	running  bool
	lastErr  string
	ctx      context.Context
	stop     context.CancelFunc
	inflight context.CancelFunc
	wake     chan struct{}
	wg       sync.WaitGroup
}

func New(db *database.DB, store *auth.Store, execute ExecuteFunc, route RouteFunc, failure FailureFunc) *Manager {
	return &Manager{db: db, store: store, execute: execute, route: route, failure: failure, now: time.Now, localIPs: LocalIPv6,
		config: DefaultConfig(), entries: map[string]Entry{}, wake: make(chan struct{}, 1)}
}

func key(id int64, model string) string { return fmt.Sprintf("%d/%s", id, model) }

func encode(value any) string { data, _ := json.Marshal(value); return string(data) }

func (m *Manager) Start(ctx context.Context) error {
	if err := m.db.InitIPv6State(ctx); err != nil {
		return err
	}
	raw, err := m.db.LoadIPv6StateConfig(ctx)
	if err != nil {
		return err
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &m.config); err != nil {
			return errors.New("invalid saved IPv6 state configuration")
		}
		if err := m.config.Validate(); err != nil {
			return err
		}
	}
	rows, err := m.db.ListIPv6States(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var entry Entry
		if json.Unmarshal([]byte(row.Data), &entry) != nil || entry.AccountID != row.AccountID || entry.Model != row.Model {
			return errors.New("invalid saved IPv6 state entry")
		}
		entry.Value = row.Secret
		m.entries[key(entry.AccountID, entry.Model)] = entry
	}
	m.ctx, m.stop = context.WithCancel(ctx)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.ctx.Done():
				return
			case <-ticker.C:
			case <-m.wake:
			}
			m.step()
		}
	}()
	return nil
}

func (m *Manager) Stop() {
	if m.stop != nil {
		m.stop()
	}
	m.wg.Wait()
}

func (m *Manager) Configure(ctx context.Context, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.db.SaveIPv6StateConfig(ctx, encode(config)); err != nil {
		return err
	}
	if m.inflight != nil {
		m.inflight()
	}
	config.AccountIDs = slices.Clone(config.AccountIDs)
	config.ProxyIDs = slices.Clone(config.ProxyIDs)
	config.Models = slices.Clone(config.Models)
	config.SourceIPs = slices.Clone(config.SourceIPs)
	m.config = config
	m.revision++
	m.nextAt = 0
	m.lastErr = ""
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return nil
}

func (m *Manager) selected(account *auth.Account, model string) bool {
	return account != nil && !account.IsRelayStyle() && !account.IsCodexAgentIdentity() &&
		(len(m.config.AccountIDs) == 0 || slices.Contains(m.config.AccountIDs, account.ID())) && slices.Contains(m.config.Models, model)
}

func (m *Manager) Applies(account *auth.Account, model string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config.Enabled && m.selected(account, model)
}

func (m *Manager) Guard(account *auth.Account, model, incoming string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.config.Enabled || incoming == "" {
		return nil
	}
	for _, entry := range m.entries {
		if entry.Value != incoming {
			continue
		}
		identity, err := statepool.Snapshot(account, "")
		if err != nil || identity != entry.Identity || model != entry.Model {
			return errors.New("state_account_or_model_mismatch")
		}
	}
	return nil
}

func valid(entry Entry, identity statepool.Identity, now time.Time) bool {
	issued, expires, err := TokenTimes(entry.Value, now)
	return err == nil && entry.Identity == identity && entry.IssuedAt == issued && entry.ExpiresAt == expires && entry.Fingerprint == statepool.Hash(entry.Value)
}

// Empty state with active=true clears client state until a matching token exists.
func (m *Manager) Resolve(account *auth.Account, model string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.config.Enabled || !m.selected(account, model) {
		return "", false, nil
	}
	identity, err := statepool.Snapshot(account, "")
	if err != nil {
		return "", true, errors.New("ipv6_state_identity_unavailable")
	}
	entry := m.entries[key(account.ID(), model)]
	if valid(entry, identity, m.now()) {
		return entry.Value, true, nil
	}
	return "", true, nil
}

func (m *Manager) combinations() []Entry {
	result := []Entry{}
	for _, account := range m.store.Accounts() {
		for _, model := range m.config.Models {
			if !m.selected(account, model) {
				continue
			}
			identity, err := statepool.Snapshot(account, "")
			if err != nil {
				continue
			}
			entry := m.entries[key(account.ID(), model)]
			if entry.Identity != identity {
				entry = Entry{AccountID: account.ID(), Model: model, Identity: identity}
			}
			account.Mu().RLock()
			entry.AccountName = account.Email
			account.Mu().RUnlock()
			if valid(entry, identity, m.now()) {
				entry.Status = "ready"
			} else if !account.IsAvailable() || account.ModelCooldownRemaining(model) > 0 {
				entry.Status = "account_unavailable"
			} else if entry.Status == "" || entry.Status == "ready" || entry.Status == "account_unavailable" {
				entry.Status = "waiting"
			}
			result = append(result, entry)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AccountID != result[j].AccountID {
			return result[i].AccountID < result[j].AccountID
		}
		return result[i].Model < result[j].Model
	})
	return result
}

func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ips, _ := m.localIPs()
	config := m.config
	config.ProxyIDs = slices.Clone(config.ProxyIDs)
	config.AccountIDs, config.Models, config.SourceIPs = slices.Clone(config.AccountIDs), slices.Clone(config.Models), slices.Clone(config.SourceIPs)
	entries := m.combinations()
	for i := range entries {
		entries[i].Value = ""
	}
	return Status{Config: config, Entries: entries, LocalIPs: ips, Running: m.running, Error: m.lastErr, ServerTime: m.now().Unix()}
}

func retryAt(header string, now time.Time) int64 {
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds > 0 && seconds < 1<<40 {
		return now.Unix() + seconds
	}
	if date, err := http.ParseTime(header); err == nil && date.After(now) {
		return date.Unix()
	}
	return now.Add(time.Minute).Unix()
}

func (m *Manager) save(ctx context.Context, entry Entry) error {
	return m.db.SaveIPv6State(ctx, database.StatePoolRow{AccountID: entry.AccountID, Model: entry.Model, Data: encode(entry), Secret: entry.Value})
}

// The single worker rotates combinations fairly; each combination rotates its
// own source addresses. No body is read, including on non-200 responses.
func (m *Manager) step() {
	m.mu.Lock()
	if !m.config.Enabled || m.running || m.now().Unix() < m.nextAt || m.ctx.Err() != nil {
		m.mu.Unlock()
		return
	}
	entries := m.combinations()
	var entry Entry
	found := false
	for i := range entries {
		index := (m.cursor + i) % len(entries)
		candidate := entries[index]
		if candidate.Status != "ready" && candidate.Status != "account_unavailable" && candidate.RetryAt <= m.now().Unix() {
			entry, found, m.cursor = candidate, true, index+1
			break
		}
	}
	if !found {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.inflight, m.running = cancel, true
	revision := m.revision
	config := m.config
	entry.Status, entry.Error = "collecting", ""
	m.entries[key(entry.AccountID, entry.Model)] = entry
	m.mu.Unlock()

	route, routeErr := m.captureRoute(ctx, config, entry.Attempts)
	if routeErr != nil {
		entry.Status, entry.Error, entry.RetryAt = "retrying", routeErr.Error(), m.now().Add(time.Minute).Unix()
	} else {
		account := m.store.TakePreferredAccountWithFilter(entry.AccountID, 0, nil, m.store.WithModelCooldownFilter(entry.Model, nil))
		if account == nil {
			entry.Status, entry.RetryAt = "waiting", m.now().Add(3*time.Second).Unix()
		} else {
			entry.SourceIP, entry.ProxyID, entry.ProxyName, entry.SessionID = route.SourceIP, route.ProxyID, route.ProxyName, route.SessionID
			entry = m.probe(ctx, cancel, account, entry, route)
			m.store.Release(account)
		}
	}
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.running, m.inflight = false, nil
	m.nextAt = m.now().Unix() + int64(m.config.IntervalSeconds)
	if m.revision != revision || m.ctx.Err() != nil {
		old := m.entries[key(entry.AccountID, entry.Model)]
		old.Status = "waiting"
		m.entries[key(entry.AccountID, entry.Model)] = old
		return
	}
	if err := m.save(m.ctx, entry); err != nil {
		m.lastErr = "state_persistence_failed"
		return
	}
	m.entries[key(entry.AccountID, entry.Model)] = entry
	m.lastErr = ""
}

func (m *Manager) captureRoute(ctx context.Context, config Config, attempt int64) (Route, error) {
	if config.CaptureMode == "proxy" {
		if len(config.ProxyIDs) == 0 || m.route == nil {
			return Route{}, errors.New("no_capture_proxy")
		}
		return m.route(ctx, config, attempt)
	}
	ips, err := m.localIPs()
	if err != nil {
		return Route{}, errors.New("ipv6_interface_lookup_failed")
	}
	if len(config.SourceIPs) > 0 {
		ips = slices.DeleteFunc(ips, func(ip string) bool { return !slices.Contains(config.SourceIPs, ip) })
	}
	if len(ips) == 0 {
		return Route{}, errors.New("no_local_public_ipv6")
	}
	return Route{SourceIP: ips[attempt%int64(len(ips))]}, nil
}

func (m *Manager) probe(ctx context.Context, cancel context.CancelFunc, account *auth.Account, entry Entry, route Route) Entry {
	identity, err := statepool.Snapshot(account, "")
	if err != nil || identity != entry.Identity || !account.IsAvailable() || account.ModelCooldownRemaining(entry.Model) > 0 {
		entry.Status, entry.Error = "waiting", "account_changed_or_unavailable"
		return entry
	}
	entry.Attempts++
	entry.HTTPStatus, entry.LastLength = 0, 0
	response, err := m.execute(ctx, account, entry.Model, route)
	// Cancel before Close so a transport cannot drain a streaming response body.
	cancel()
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	m.mu.RLock()
	interval := m.config.IntervalSeconds
	m.mu.RUnlock()
	entry.RetryAt = m.now().Unix() + int64(interval)
	if err != nil || response == nil {
		entry.Status, entry.Error = "retrying", "capture_transport_error"
		entry.RetryAt = m.now().Add(time.Minute).Unix()
		return entry
	}
	entry.HTTPStatus = response.StatusCode
	value := response.Header.Get(Header)
	entry.LastLength = len(value)
	if response.StatusCode != http.StatusOK {
		entry.Status, entry.Error = "retrying", fmt.Sprintf("upstream_%d", response.StatusCode)
		entry.RetryAt = retryAt(response.Header.Get("Retry-After"), m.now())
		if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429 {
			if m.failure != nil {
				m.failure(account, entry.Model, response)
			}
			entry.Status = "account_unavailable"
		}
		return entry
	}
	issued, expires, err := TokenTimes(value, m.now())
	if err != nil {
		entry.Status, entry.Error = "retrying", err.Error()
		return entry
	}
	identity, err = statepool.Snapshot(account, "")
	if err != nil || identity != entry.Identity {
		entry.Status, entry.Error = "waiting", "account_identity_changed"
		return entry
	}
	entry.Value, entry.Fingerprint = value, statepool.Hash(value)
	entry.IssuedAt, entry.ExpiresAt, entry.CapturedAt = issued, expires, m.now().Unix()
	entry.Status, entry.Error, entry.RetryAt = "ready", "", 0
	return entry
}

func (m *Manager) Export(id int64, model string) (Portable, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.entries[key(id, model)]
	identity, err := statepool.Snapshot(m.store.FindByID(id), "")
	if err != nil || !valid(entry, identity, m.now()) {
		return Portable{}, errors.New("no valid matching state")
	}
	return Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: model, Value: entry.Value}, nil
}

func (m *Manager) Import(ctx context.Context, pack Portable) error {
	if pack.Format != "codex2api-ipv6-292-v1" {
		return errors.New("unsupported IPv6 state package")
	}
	model, err := statepool.NormalizeModel(pack.Model)
	if err != nil || model != pack.Model {
		return errors.New("invalid exact model")
	}
	issued, expires, err := TokenTimes(pack.Value, m.now())
	if err != nil {
		return err
	}
	var match *auth.Account
	var identity statepool.Identity
	for _, account := range m.store.Accounts() {
		candidate, err := statepool.Snapshot(account, "")
		if err == nil && candidate.MemberHash == pack.MemberHash && candidate.WorkspaceHash == pack.WorkspaceHash {
			if match != nil {
				return errors.New("ambiguous account identity")
			}
			match, identity = account, candidate
		}
	}
	if match == nil {
		return errors.New("matching account member and workspace not found")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := Entry{AccountID: match.ID(), Model: model, Identity: identity, Value: pack.Value,
		Fingerprint: statepool.Hash(pack.Value), IssuedAt: issued, ExpiresAt: expires, CapturedAt: m.now().Unix(), Status: "ready"}
	if err := m.save(ctx, entry); err != nil {
		return err
	}
	if m.inflight != nil {
		m.inflight()
	}
	m.revision++
	m.entries[key(entry.AccountID, entry.Model)] = entry
	return nil
}
