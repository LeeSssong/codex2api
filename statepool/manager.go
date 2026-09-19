package statepool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/google/uuid"
)

type ExecuteFunc func(context.Context, *auth.Account, []byte, string, http.Header) (*http.Response, error)
type FailureFunc func(*auth.Account, string, *http.Response, []byte)

type Manager struct {
	db        *database.DB
	store     *auth.Store
	execute   ExecuteFunc
	failure   FailureFunc
	now       func() time.Time
	owner     string
	mu        sync.RWMutex
	entries   map[string]Entry
	cancels   map[string]context.CancelFunc
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	refreshMu sync.Mutex
}

func New(db *database.DB, store *auth.Store, execute ExecuteFunc, failure FailureFunc) *Manager {
	return &Manager{db: db, store: store, execute: execute, failure: failure, now: time.Now,
		owner: uuid.NewString(), entries: map[string]Entry{}, cancels: map[string]context.CancelFunc{}}
}

func key(accountID int64, model, effort string) string {
	return fmt.Sprintf("%d/%s/%s", accountID, model, effort)
}

func (m *Manager) refresh(ctx context.Context) error {
	m.refreshMu.Lock()
	defer m.refreshMu.Unlock()
	rows, err := m.db.ListStatePoolEntries(ctx)
	if err != nil {
		return err
	}
	entries := map[string]Entry{}
	for _, row := range rows {
		var entry Entry
		if err := json.Unmarshal([]byte(row.Data), &entry); err != nil {
			return err
		}
		entry.Value = row.Secret
		entries[key(entry.AccountID, entry.Model, entry.Effort)] = entry
	}
	m.mu.Lock()
	m.entries = entries
	m.mu.Unlock()
	return nil
}

func (m *Manager) Start(parent context.Context) error {
	if err := m.refresh(parent); err != nil {
		return err
	}
	m.ctx, m.cancel = context.WithCancel(parent)
	m.wg.Add(1)
	go m.loop()
	return nil
}

func (m *Manager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

func (m *Manager) loop() {
	defer m.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			_ = m.refresh(m.ctx)
			for range 32 {
				row, err := m.db.ClaimStatePoolJob(m.ctx, m.owner, m.now())
				if err != nil || row == nil {
					break
				}
				m.wg.Add(1)
				go func() { defer m.wg.Done(); m.run(*row) }()
			}
		}
	}
}

func encode(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func (m *Manager) accountJob(accountID int64, model, batch string, enable, strict bool) (Job, error) {
	account := m.store.FindByID(accountID)
	identity, err := Snapshot(account, m.store.ResolveProxyForAccount(account))
	if err != nil {
		return Job{}, err
	}
	model, err = NormalizeModel(model)
	if err != nil {
		return Job{}, err
	}
	account.Mu().RLock()
	name := account.Email
	account.Mu().RUnlock()
	now := m.now().Unix()
	id := uuid.NewString()
	return Job{ID: id, GroupID: id, Candidate: 1, Candidates: 1, Strategy: "race", BatchID: batch, AccountID: accountID, AccountName: name,
		Model: model, Effort: "high", Identity: identity, Status: "queued", Phase: "queued",
		CreatedAt: now, UpdatedAt: now, Enable: enable, Strict: strict, Source: "capture", Checks: []Check{}}, nil
}

func jobRow(job Job, secret string) database.StatePoolRow {
	return database.StatePoolRow{ID: job.ID, AccountID: job.AccountID, Model: job.Model, Effort: job.Effort,
		Status: job.Status, Data: encode(job), Secret: secret, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		GroupID: job.GroupID, Ordinal: job.Candidate, Serial: job.Strategy == "sequential"}
}

func (m *Manager) Capture(ctx context.Context, accounts []int64, models []string, enable, strict bool, captureOptions ...CaptureOptions) ([]string, error) {
	if len(accounts) == 0 || len(models) == 0 || len(accounts)*len(models) > 128 {
		return nil, errors.New("select accounts and models (maximum 128 groups per batch)")
	}
	options := CaptureOptions{Candidates: 1, Strategy: "race"}
	if len(captureOptions) > 0 {
		options = captureOptions[0]
		if options.Candidates == 0 {
			options.Candidates = 1
		}
		if options.Strategy == "" {
			options.Strategy = "race"
		}
	}
	if options.Candidates < 1 || options.Candidates > 12 || options.Strategy != "race" && options.Strategy != "sequential" {
		return nil, errors.New("use 1 to 12 candidates with race or sequential strategy")
	}
	routes, err := m.captureRoutes(ctx, options)
	if err != nil {
		return nil, err
	}
	if len(routes) > options.Candidates {
		return nil, errors.New("candidate budget must cover every selected proxy")
	}
	forward, err := m.captureForwardRoute(ctx, options)
	if err != nil {
		return nil, err
	}
	batch := uuid.NewString()
	rows := []database.StatePoolRow{}
	sessions := map[string]bool{}
	for _, id := range accounts {
		for _, model := range models {
			job, err := m.accountJob(id, model, batch, enable, strict)
			if err != nil {
				return nil, fmt.Errorf("account %d: %w", id, err)
			}
			if existing, ok := m.reusableEntry(job); ok {
				job.Source, job.CapturedAt, job.ExpiresAt = "reuse", existing.CapturedAt, existing.ExpiresAt
				rows = append(rows, jobRow(job, existing.Value))
				continue
			}
			for candidate := 1; candidate <= options.Candidates; candidate++ {
				copy := job
				copy.ForwardProxy = forward
				copy.ID = uuid.NewString()
				copy.Candidate, copy.Candidates, copy.Strategy = candidate, options.Candidates, options.Strategy
				if len(routes) > 0 {
					copy.CaptureProxy = routes[(candidate-1)%len(routes)]
					if copy.CaptureProxy.RotateSession {
						for {
							session, err := newProxySession()
							if err != nil {
								return nil, err
							}
							if !sessions[session] {
								sessions[session] = true
								copy.CaptureProxy.SessionID = session
								break
							}
						}
					}
				}
				rows = append(rows, jobRow(copy, ""))
			}
		}
	}
	return m.db.CreateStatePoolJobs(ctx, rows)
}

func (m *Manager) Entries() []Entry {
	m.mu.RLock()
	entries := make([]Entry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, entry)
	}
	m.mu.RUnlock()
	for index := range entries {
		entry := &entries[index]
		entry.Status = "ready"
		account := m.store.FindByID(entry.AccountID)
		identity, err := Snapshot(account, m.store.ResolveProxyForAccount(account))
		if err != nil || identity != entry.Identity {
			entry.Status = "identity_changed"
		} else if entry.ExpiresAt <= m.now().Unix() {
			entry.Status = "expired"
		} else if !entry.Enabled {
			entry.Status = "disabled"
		}
		entry.Value = ""
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].AccountID != entries[j].AccountID {
			return entries[i].AccountID < entries[j].AccountID
		}
		return entries[i].Model < entries[j].Model
	})
	return entries
}

func (m *Manager) Jobs(ctx context.Context) ([]Job, error) {
	rows, err := m.db.ListStatePoolJobs(ctx)
	if err != nil {
		return nil, err
	}
	jobs := []Job{}
	for _, row := range rows {
		var job Job
		if err := json.Unmarshal([]byte(row.Data), &job); err != nil {
			return nil, err
		}
		job.Status, job.UpdatedAt = row.Status, row.UpdatedAt
		job.GroupID = row.GroupID
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (m *Manager) Cancel(ctx context.Context, id string) error {
	if err := m.db.CancelStatePoolJob(ctx, id); err != nil {
		return err
	}
	m.mu.RLock()
	cancel := m.cancels[id]
	m.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (m *Manager) entry(id string) (Entry, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, entry := range m.entries {
		if entry.ID == id {
			return entry, nil
		}
	}
	return Entry{}, errors.New("state entry not found")
}

func (m *Manager) Configure(ctx context.Context, id string, enabled, strict bool) error {
	entry, err := m.entry(id)
	if err != nil {
		return err
	}
	if enabled && entry.ExpiresAt <= m.now().Unix() {
		return errors.New("expired state must be recaptured")
	}
	if enabled {
		account := m.store.FindByID(entry.AccountID)
		identity, err := Snapshot(account, m.store.ResolveProxyForAccount(account))
		if err != nil || identity != entry.Identity {
			return errors.New("account identity changed; state must be revalidated")
		}
	}
	entry.Enabled, entry.Strict = enabled, strict
	if err := m.db.UpdateStatePoolEntry(ctx, id, encode(entry)); err != nil {
		return err
	}
	return m.refresh(ctx)
}

func (m *Manager) Delete(ctx context.Context, id string) error {
	if err := m.db.DeleteStatePoolEntry(ctx, id); err != nil {
		return err
	}
	return m.refresh(ctx)
}

// Native continuation wins. Only locally validated entries can supply new state.
func (m *Manager) Resolve(account *auth.Account, model, effort, proxyURL, native string) (string, error) {
	if account == nil {
		return "", nil
	}
	m.mu.RLock()
	entry, found := m.entries[key(account.ID(), model, effort)]
	if native != "" {
		for _, known := range m.entries {
			if known.Value == native && (known.AccountID != account.ID() || known.Model != model) {
				m.mu.RUnlock()
				return "", errors.New("verified_state_identity_mismatch")
			}
		}
		m.mu.RUnlock()
		return "", nil
	}
	m.mu.RUnlock()
	if !found || !entry.Enabled {
		return "", nil
	}
	identity, err := Snapshot(account, proxyURL)
	if err != nil || identity != entry.Identity || entry.ExpiresAt <= m.now().Unix() || entry.Value == "" {
		if entry.Strict {
			return "", errors.New("verified_state_unavailable")
		}
		return "", nil
	}
	return entry.Value, nil
}

func (m *Manager) Export(ids []string) (Package, error) {
	if len(ids) == 0 || len(ids) > 128 {
		return Package{}, errors.New("select 1 to 128 state entries")
	}
	result := Package{Format: "codex2api-state", Version: 1, ExportedAt: m.now().Unix(), States: []PortableState{}}
	for _, id := range ids {
		entry, err := m.entry(id)
		if err != nil {
			return Package{}, err
		}
		state := PortableState{MemberHash: entry.Identity.MemberHash, WorkspaceHash: entry.Identity.WorkspaceHash,
			Model: entry.Model, Effort: entry.Effort, Value: entry.Value, Fingerprint: entry.Fingerprint,
			CapturedAt: entry.CapturedAt, ExpiresAt: entry.ExpiresAt, Benchmark: entry.Benchmark}
		if err := ValidatePortable(state, m.now()); err != nil {
			return Package{}, err
		}
		result.States = append(result.States, state)
	}
	return result, nil
}

func (m *Manager) Import(ctx context.Context, pack Package, enable, strict bool, allowPartial ...bool) ([]string, error) {
	items, err := m.PreviewImport(pack)
	if err != nil {
		return nil, err
	}
	partial := len(allowPartial) > 0 && allowPartial[0]
	for _, item := range items {
		if item.Status == "invalid" && !partial {
			return nil, fmt.Errorf("%s: %s", item.Model, item.Reason)
		}
	}
	batch := uuid.NewString()
	rows := []database.StatePoolRow{}
	for _, item := range items {
		if item.Status == "invalid" {
			continue
		}
		if item.Status == "already_verified" {
			if err := m.Configure(ctx, item.ExistingID, enable, strict); err != nil {
				return nil, err
			}
			continue
		}
		state := pack.States[item.Index]
		job, err := m.accountJob(item.AccountID, state.Model, batch, enable, strict)
		if err != nil {
			return nil, err
		}
		if job.Identity.MemberHash != state.MemberHash || job.Identity.WorkspaceHash != state.WorkspaceHash {
			return nil, errors.New("account identity changed during import")
		}
		job.Source, job.CapturedAt, job.ExpiresAt = "import", state.CapturedAt, state.ExpiresAt
		rows = append(rows, jobRow(job, state.Value))
	}
	if len(rows) == 0 {
		return []string{}, nil
	}
	return m.db.CreateStatePoolJobs(ctx, rows)
}

func (m *Manager) ImportRaw(ctx context.Context, accountID int64, model, value string, capturedAt int64, enable, strict bool) ([]string, error) {
	job, err := m.accountJob(accountID, model, uuid.NewString(), enable, strict)
	if err != nil {
		return nil, err
	}
	state := PortableState{MemberHash: job.Identity.MemberHash, WorkspaceHash: job.Identity.WorkspaceHash,
		Model: job.Model, Effort: job.Effort, Value: value, Fingerprint: Hash(value), CapturedAt: capturedAt,
		ExpiresAt: capturedAt + int64(Lifetime.Seconds())}
	if err := ValidatePortable(state, m.now()); err != nil {
		return nil, err
	}
	job.Source, job.CapturedAt, job.ExpiresAt = "raw_import", state.CapturedAt, state.ExpiresAt
	return m.db.CreateStatePoolJobs(ctx, []database.StatePoolRow{jobRow(job, state.Value)})
}

func (m *Manager) run(row database.StatePoolRow) {
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	m.mu.Lock()
	m.cancels[row.ID] = cancel
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.cancels, row.ID); m.mu.Unlock() }()
	var job Job
	if json.Unmarshal([]byte(row.Data), &job) != nil {
		return
	}
	job.GroupID = row.GroupID
	job.Status = "running"
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				alive, err := m.db.HeartbeatStatePoolJob(ctx, job.ID, row.Owner, m.now())
				if err != nil || !alive {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-heartbeatDone }()
	progress := func() error {
		row.Status, row.Data = "running", encode(job)
		alive, err := m.db.UpdateStatePoolJob(ctx, row, m.now())
		if err != nil {
			return err
		}
		if !alive {
			return context.Canceled
		}
		return nil
	}
	entry, err := m.validate(ctx, &job, row.Secret, progress)
	if err == nil {
		job.EntryID, job.Phase = entry.ID, "completed"
		row.Data = encode(job)
		err = m.db.PublishStatePoolEntry(ctx, database.StatePoolRow{ID: entry.ID, AccountID: entry.AccountID,
			Model: entry.Model, Effort: entry.Effort, Data: encode(entry), Secret: entry.Value},
			entry.Identity.CredentialGeneration, row, m.now())
		if err == nil {
			_ = m.refresh(ctx)
			m.cancelStopped(ctx)
			return
		}
	}
	job.Status, job.Error = "failed", err.Error()
	if ctx.Err() != nil {
		job.Status, job.Error = "interrupted", "task cancelled or server stopped"
	}
	row.Status, row.Data = job.Status, encode(job)
	_, _ = m.db.UpdateStatePoolJob(context.Background(), row, m.now())
	_ = m.db.FinishCancelledStatePoolJob(context.Background(), row.ID, row.Owner)
}

func (m *Manager) validate(ctx context.Context, job *Job, value string, progress func() error) (Entry, error) {
	if value == "" {
		job.Phase = "capturing"
		if err := progress(); err != nil {
			return Entry{}, err
		}
		check, state, captured := m.call(ctx, *job, "capture", "", CapturePrompt)
		job.Checks = append(job.Checks, check)
		job.RetryAt = check.RetryAt
		if err := progress(); err != nil {
			return Entry{}, err
		}
		if !check.Passed || state == "" {
			return Entry{}, errors.New(firstError(check.Error, "capture_failed_or_missing_state"))
		}
		value, job.CapturedAt, job.ExpiresAt = state, captured, captured+int64(Lifetime.Seconds())
	}
	if m.now().Unix() >= job.ExpiresAt {
		return Entry{}, errors.New("state_expired")
	}
	if err := ValidatePortable(PortableState{MemberHash: job.Identity.MemberHash, WorkspaceHash: job.Identity.WorkspaceHash,
		Model: job.Model, Effort: job.Effort, Value: value, Fingerprint: Hash(value), CapturedAt: job.CapturedAt,
		ExpiresAt: job.ExpiresAt}, m.now()); err != nil {
		return Entry{}, err
	}
	job.Phase = "verifying"
	if err := progress(); err != nil {
		return Entry{}, err
	}
	check, _, _ := m.call(ctx, *job, "verify", value, VerifyPrompt)
	job.Checks = append(job.Checks, check)
	job.RetryAt = check.RetryAt
	if err := progress(); err != nil {
		return Entry{}, err
	}
	if !check.Passed {
		if job.Source == "reuse" && (check.Error == "benchmark_failed" || check.Error == "invalid_answer_format" || check.Error == "HTTP 400: invalid_turn_state" || check.Error == "HTTP 400: turn_state_expired") {
			job.Source, job.CapturedAt, job.ExpiresAt = "capture", 0, 0
			return m.validate(ctx, job, "", progress)
		}
		return Entry{}, errors.New(firstError(check.Error, "verification_failed"))
	}
	if m.now().Unix() >= job.ExpiresAt {
		return Entry{}, errors.New("state_expired")
	}
	account := m.store.FindByID(job.AccountID)
	identity, err := Snapshot(account, m.store.ResolveProxyForAccount(account))
	if err != nil || identity != job.Identity {
		return Entry{}, errors.New("account_identity_changed")
	}
	return Entry{ID: uuid.NewString(), AccountID: job.AccountID, AccountName: job.AccountName, Model: job.Model,
		Effort: job.Effort, Identity: identity, Value: value, Fingerprint: Hash(value), CapturedAt: job.CapturedAt,
		ExpiresAt: job.ExpiresAt, VerifiedAt: m.now().Unix(), Enabled: job.Enable, Strict: job.Strict,
		Source: job.Source, Benchmark: Benchmark, Checks: job.Checks, Status: "ready"}, nil
}

func firstError(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
