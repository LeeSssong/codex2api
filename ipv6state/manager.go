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

type captureTask struct {
	id       uint64
	ctx      context.Context
	cancel   context.CancelFunc
	entry    Entry
	account  *auth.Account
	config   Config
	revision uint64
}

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
	nextTask uint64
	active   map[uint64]*captureTask
	lastErr  string
	ctx      context.Context
	stop     context.CancelFunc
	wake     chan struct{}
	wg       sync.WaitGroup
	workers  sync.WaitGroup
}

func New(db *database.DB, store *auth.Store, execute ExecuteFunc, route RouteFunc, failure FailureFunc) *Manager {
	return &Manager{db: db, store: store, execute: execute, route: route, failure: failure, now: time.Now, localIPs: LocalIPv6,
		config: DefaultConfig(), entries: map[string]Entry{}, active: map[uint64]*captureTask{}, wake: make(chan struct{}, 1)}
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
		// Existing installations retain their serial limit until explicitly changed.
		m.config.Concurrency = 1
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
	m.workers.Wait()
	for _, account := range m.store.Accounts() {
		account.SetStateBusinessLimit(0)
	}
}

func (m *Manager) Configure(ctx context.Context, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// The system policy has a dedicated patch endpoint. Capture settings from a
	// stale browser tab must not silently disable strict dispatch.
	config.RequireValidState = m.config.RequireValidState
	if err := m.db.SaveIPv6StateConfig(ctx, encode(config)); err != nil {
		return err
	}
	for _, task := range m.active {
		task.cancel()
	}
	config.AccountIDs = slices.Clone(config.AccountIDs)
	config.ProxyIDs = slices.Clone(config.ProxyIDs)
	config.Models = slices.Clone(config.Models)
	config.SourceIPs = slices.Clone(config.SourceIPs)
	config.AcceptedLengths = slices.Clone(config.AcceptedLengths)
	m.config = config
	m.updateBusinessBudgets()
	m.revision++
	m.lastErr = ""
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return nil
}

func (m *Manager) selected(account *auth.Account, model string) bool {
	return NativeAccount(account) &&
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

func valid(entry Entry, identity statepool.Identity, now time.Time, config Config) bool {
	issued, expires, err := TokenTimes(entry.Value, now)
	return config.accepts(entry.Value) && err == nil && entry.Identity == identity && entry.IssuedAt == issued && entry.ExpiresAt == expires && entry.Fingerprint == statepool.Hash(entry.Value)
}

// Empty state with active=true clears client state until a matching token exists.
func (m *Manager) Resolve(account *auth.Account, model string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.config.Enabled || !m.selected(account, model) {
		if m.config.RequireValidState && NativeAccount(account) {
			return "", true, ErrStateRequired
		}
		return "", false, nil
	}
	identity, err := statepool.Snapshot(account, "")
	if err != nil {
		return "", true, errors.New("ipv6_state_identity_unavailable")
	}
	entry := m.entries[key(account.ID(), model)]
	if valid(entry, identity, m.now(), m.config) {
		return entry.Value, true, nil
	}
	if m.config.RequireValidState {
		return "", true, ErrStateRequired
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
			entry := m.observe(account, model, identity, err, m.now())
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
	config.AcceptedLengths = slices.Clone(config.AcceptedLengths)
	config.ProxyIDs = slices.Clone(config.ProxyIDs)
	config.AccountIDs, config.Models, config.SourceIPs = slices.Clone(config.AccountIDs), slices.Clone(config.Models), slices.Clone(config.SourceIPs)
	entries := m.combinations()
	for i := range entries {
		entries[i].Value = ""
	}
	return Status{Summary: m.snapshot(m.now()).Summary, Config: config, Entries: entries, LocalIPs: ips, Running: len(m.active) > 0, ActiveRequests: len(m.active), AccountConcurrency: m.store.GetMaxConcurrency(), Error: m.lastErr, ServerTime: m.now().Unix()}
}

func retryAt(header string, now time.Time) int64 {
	if retry, ok := parsedRetryAfter(header, now); ok {
		return retry
	}
	return now.Add(time.Minute).Unix()
}

func parsedRetryAfter(header string, now time.Time) (int64, bool) {
	if seconds, err := strconv.ParseInt(header, 10, 64); err == nil && seconds > 0 && seconds < 1<<40 {
		return now.Unix() + seconds, true
	}
	if date, err := http.ParseTime(header); err == nil && date.After(now) {
		return date.Unix(), true
	}
	return 0, false
}

func (m *Manager) save(ctx context.Context, entry Entry) error {
	return m.db.SaveIPv6State(ctx, database.StatePoolRow{AccountID: entry.AccountID, Model: entry.Model, Data: encode(entry), Secret: entry.Value})
}

// Caller holds m.mu. Cancelled workers remain in active until they release their slot.
func (m *Manager) collecting(id int64, model string) bool {
	for _, task := range m.active {
		if task.entry.AccountID == id && task.entry.Model == model && task.ctx.Err() == nil {
			return true
		}
	}
	return false
}

// Fill the global worker budget fairly across account/model pairs. Each pair may
// use several connections, while the account scheduler still enforces its limit.
func (m *Manager) step() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateBusinessBudgets()
	for _, task := range m.active {
		account := m.store.FindByID(task.entry.AccountID)
		identity, err := statepool.Snapshot(account, "")
		available, _, _ := account.StateAvailability(task.entry.Model, m.now())
		if !m.config.Enabled || err != nil || identity != task.entry.Identity || !available || !m.selected(account, task.entry.Model) {
			task.cancel()
		}
	}
	if !m.config.Enabled || m.ctx.Err() != nil {
		return
	}
	for len(m.active) < m.config.Concurrency {
		entries := m.combinations()
		var selected *captureTask
		for i := range entries {
			index := (m.cursor + i) % len(entries)
			entry := entries[index]
			if entry.Valid && entry.ExpiresAt > m.now().Add(m.config.refreshBefore()).Unix() || entry.CapturePhase == "account_unavailable" || entry.RetryAt > m.now().Unix() {
				continue
			}
			account := m.store.FindByID(entry.AccountID)
			if account == nil {
				continue
			}
			limit, active := m.captureBudget(account)
			if active >= limit {
				continue
			}
			account = m.store.TakeStateCaptureAccount(entry.AccountID, m.store.WithModelCooldownFilter(entry.Model, nil))
			if account == nil {
				continue
			}
			ctx, cancel := context.WithCancel(m.ctx)
			m.nextTask++
			entry.Attempts++
			entry.Status = "collecting"
			m.entries[key(entry.AccountID, entry.Model)] = entry
			selected = &captureTask{id: m.nextTask, ctx: ctx, cancel: cancel, entry: entry, account: account, config: m.config, revision: m.revision}
			m.active[selected.id] = selected
			m.cursor = index + 1
			break
		}
		if selected == nil {
			return
		}
		m.workers.Add(1)
		go m.run(selected)
	}
}

func (m *Manager) run(task *captureTask) {
	defer m.workers.Done()
	defer func() {
		task.cancel()
		m.store.ReleaseStateCapture(task.account)
		m.mu.Lock()
		delete(m.active, task.id)
		m.mu.Unlock()
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}()
	entry := task.entry
	ctx, cancel := context.WithCancel(task.ctx)
	defer cancel()
	if ctx.Err() != nil {
		return
	}
	route, routeErr := m.captureRoute(ctx, task.config, entry.Attempts-1)
	var response *http.Response
	if routeErr != nil {
		entry.Status, entry.Error, entry.RetryAt = "retrying", routeErr.Error(), m.now().Add(time.Minute).Unix()
		entry.RetrySource = "local_backoff"
	} else {
		entry.SourceIP, entry.ProxyID, entry.ProxyName, entry.SessionID = route.SourceIP, route.ProxyID, route.ProxyName, route.SessionID
		entry, response = m.probe(ctx, cancel, task.account, entry, route, task.config)
	}
	cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	if task.ctx.Err() != nil || task.revision != m.revision {
		return
	}
	identity, identityErr := statepool.Snapshot(m.store.FindByID(entry.AccountID), "")
	if identityErr != nil || identity != entry.Identity {
		return
	}
	if entry.Status == "ready" {
		if available, _, _ := task.account.StateAvailability(entry.Model, m.now()); !available {
			return
		}
	}
	current := m.entries[key(entry.AccountID, entry.Model)]
	currentValid := valid(current, identity, m.now(), m.config)
	if currentValid && (current.Fingerprint != task.entry.Fingerprint || current.ExpiresAt > m.now().Add(m.config.refreshBefore()).Unix()) {
		return
	}
	if currentValid && entry.Status == "ready" && (entry.ExpiresAt <= current.ExpiresAt || entry.ExpiresAt <= m.now().Add(m.config.refreshBefore()).Unix()) {
		entry.Value, entry.Fingerprint = current.Value, current.Fingerprint
		entry.IssuedAt, entry.ExpiresAt, entry.CapturedAt = current.IssuedAt, current.ExpiresAt, current.CapturedAt
		entry.UpdatedAt = current.UpdatedAt
		entry.Status, entry.Error, entry.RetryAt = "retrying", "state_not_newer", m.now().Unix()+int64(task.config.IntervalSeconds)
		entry.RetrySource = "local_backoff"
	}
	if entry.Status == "ready" && current.Value != "" && entry.Fingerprint != current.Fingerprint {
		entry.UpdatedAt = m.now().Unix()
	}
	entry.Attempts = current.Attempts
	persistenceFailed := false
	if entry.Status == "account_unavailable" {
		if m.failure != nil {
			m.failure(task.account, entry.Model, response)
		}
		if reason, until := task.account.GetCooldownSnapshot(); until.After(m.now()) {
			entry.CooldownReason, entry.CooldownUntil = reason, until.Unix()
			entry.RetryAt = max(entry.RetryAt, until.Unix())
			if (reason == "rate_limited_5h" || reason == "rate_limited_7d" || reason == "usage_limited" || reason == "usage_exhausted") && until.Unix() > retryAt(response.Header.Get("Retry-After"), m.now()) {
				entry.RetrySource = "usage_window"
			}
		}
		// A header-level rejection pauses all models for this account, including
		// peers whose successful headers have arrived but are not yet committed.
		for _, peer := range m.active {
			if peer.entry.AccountID == entry.AccountID {
				peer.cancel()
			}
		}
		for _, other := range m.combinations() {
			if other.AccountID != entry.AccountID || other.Model == entry.Model {
				continue
			}
			other.RetryAt = max(other.RetryAt, entry.RetryAt)
			if other.Status != "ready" {
				other.Status, other.Error = "account_unavailable", entry.Error
			}
			m.entries[key(other.AccountID, other.Model)] = other
			if err := m.save(m.ctx, other); err != nil {
				m.lastErr = "state_persistence_failed"
				persistenceFailed = true
			}
		}
	} else if entry.Status != "ready" {
		entry.RetryAt = max(entry.RetryAt, current.RetryAt)
	}
	if err := m.save(m.ctx, entry); err != nil {
		m.lastErr = "state_persistence_failed"
		current.Status, current.Error, current.RetryAt = "retrying", m.lastErr, m.now().Add(time.Minute).Unix()
		current.RetrySource = "local_backoff"
		m.entries[key(entry.AccountID, entry.Model)] = current
		return
	}
	m.entries[key(entry.AccountID, entry.Model)] = entry
	m.updateBusinessBudgets()
	if !persistenceFailed {
		m.lastErr = ""
	}
	if entry.Status == "ready" {
		for _, peer := range m.active {
			if peer.entry.AccountID == entry.AccountID && peer.entry.Model == entry.Model {
				peer.cancel()
			}
		}
	}
}

func (m *Manager) captureRoute(ctx context.Context, config Config, attempt int64) (Route, error) {
	if config.CaptureMode == "mixed" {
		modes := []string{"proxy", "local_ipv6"}
		if attempt%2 != 0 {
			modes[0], modes[1] = modes[1], modes[0]
		}
		for _, mode := range modes {
			config.CaptureMode = mode
			if route, err := m.captureRoute(ctx, config, attempt/2); err == nil {
				return route, nil
			}
		}
		return Route{}, errors.New("no_capture_route")
	}
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

func (m *Manager) probe(ctx context.Context, cancel context.CancelFunc, account *auth.Account, entry Entry, route Route, config Config) (Entry, *http.Response) {
	identity, err := statepool.Snapshot(account, "")
	available, _, _ := account.StateAvailability(entry.Model, m.now())
	if err != nil || identity != entry.Identity || !available {
		entry.Status, entry.Error = "waiting", "account_changed_or_unavailable"
		entry.RetryAt = m.now().Unix() + int64(config.IntervalSeconds)
		return entry, nil
	}
	if ctx.Err() != nil {
		return entry, nil
	}
	entry.HTTPStatus, entry.LastLength = 0, 0
	entry.CooldownReason, entry.CooldownUntil, entry.RetrySource = "", 0, ""
	response, err := m.execute(ctx, account, entry.Model, route)
	// Cancel before Close so a transport cannot drain a streaming response body.
	cancel()
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	entry.RetryAt = m.now().Unix() + int64(config.IntervalSeconds)
	entry.RetrySource = "local_backoff"
	if err != nil || response == nil {
		entry.Status, entry.Error = "retrying", "capture_transport_error"
		entry.RetryAt = m.now().Add(time.Minute).Unix()
		return entry, response
	}
	entry.HTTPStatus = response.StatusCode
	value := response.Header.Get(Header)
	entry.LastLength = len(value)
	if response.StatusCode != http.StatusOK {
		entry.Status, entry.Error = "retrying", fmt.Sprintf("upstream_%d", response.StatusCode)
		entry.RetryAt = retryAt(response.Header.Get("Retry-After"), m.now())
		entry.RetrySource = "local_backoff"
		if _, ok := parsedRetryAfter(response.Header.Get("Retry-After"), m.now()); ok {
			entry.RetrySource = "retry_after"
		}
		if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 429 {
			entry.Status = "account_unavailable"
		}
		return entry, response
	}
	if !config.accepts(value) {
		entry.Status, entry.Error = "retrying", "length_not_allowed"
		return entry, response
	}
	issued, expires, err := TokenTimes(value, m.now())
	if err != nil {
		entry.Status, entry.Error = "retrying", err.Error()
		return entry, response
	}
	identity, err = statepool.Snapshot(account, "")
	if err != nil || identity != entry.Identity {
		entry.Status, entry.Error = "waiting", "account_identity_changed"
		return entry, response
	}
	entry.Value, entry.Fingerprint = value, statepool.Hash(value)
	entry.IssuedAt, entry.ExpiresAt, entry.CapturedAt = issued, expires, m.now().Unix()
	entry.Status, entry.Error, entry.RetryAt = "ready", "", 0
	entry.RetrySource = ""
	return entry, response
}

func (m *Manager) Export(id int64, model string) (Portable, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry := m.entries[key(id, model)]
	identity, err := statepool.Snapshot(m.store.FindByID(id), "")
	if err != nil || !valid(entry, identity, m.now(), m.config) {
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
	currentIdentity, err := statepool.Snapshot(m.store.FindByID(match.ID()), "")
	if err != nil || currentIdentity != identity {
		return errors.New("account_identity_changed")
	}
	if !m.config.accepts(pack.Value) {
		return errors.New("length_not_allowed")
	}
	current := m.entries[key(match.ID(), model)]
	if valid(current, identity, m.now(), m.config) && current.ExpiresAt >= expires {
		return nil
	}
	entry := Entry{AccountID: match.ID(), Model: model, Identity: identity, Value: pack.Value,
		Fingerprint: statepool.Hash(pack.Value), IssuedAt: issued, ExpiresAt: expires, CapturedAt: m.now().Unix(), LastLength: len(pack.Value), Status: "ready"}
	if current.Value != "" {
		entry.UpdatedAt = m.now().Unix()
	}
	if err := m.save(ctx, entry); err != nil {
		return err
	}
	for _, task := range m.active {
		if task.entry.AccountID == entry.AccountID && task.entry.Model == entry.Model {
			task.cancel()
		}
	}
	m.entries[key(entry.AccountID, entry.Model)] = entry
	m.updateBusinessBudgets()
	return nil
}
