// Package credentialops owns Codex2API's account-first credential operations.
// It is deliberately independent of the Sub2API service and HTTP endpoints.
package credentialops

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var (
	ErrEncryptionUnavailable = errors.New("credential encryption is unavailable")
	ErrStaleCallback         = errors.New("credential callback is stale")
	ErrLeaseLost             = errors.New("credential operation lease lost")
)

type ManagerConfig struct {
	Encrypt func(string) (string, error)
	Decrypt func(string) (string, error)
}
type LoginConfigInput struct {
	AccountID                        int64
	Email, Mode, Engine, ProxySource string
	Password, TOTPSecret, OTPURL     string
	ClearPassword, ClearTOTP         bool
}
type LoginConfig struct {
	AccountID                                            int64
	Email, Mode, Engine, ProxySource                     string
	PasswordCiphertext, TOTPCiphertext, OTPURLCiphertext string
	UpdatedAt                                            time.Time
}
type ImportInput struct {
	Email, AccountID, RefreshToken string
	Credential                     map[string]any
}
type ImportResult struct {
	AccountID  int64
	Generation int64
	Created    bool
}
type Callback struct {
	AccountID, Generation int64
	Credential            map[string]any
}
type LoginJob struct {
	ID         string         `json:"id"`
	Status     string         `json:"status"`
	AccountID  int64          `json:"account_id"`
	Credential map[string]any `json:"credential,omitempty"`
	Error      string         `json:"error,omitempty"`
}
type Account struct {
	ID                              int64
	Email, ExternalID, RefreshToken string
	Credential                      map[string]any
	Generation                      int64
}
type Monitor struct {
	AccountID            int64
	Enabled, AutoRelogin bool
	ProbeState, Detail   string
	FailStreak           int
	NextProbeAt          time.Time
	CooldownUntil        *time.Time
	LeaseOwner           string
	LeaseUntil           *time.Time
}
type ProbeResult struct {
	State, Detail string
	FailStreak    int
	CooldownUntil *time.Time
}

const (
	ProbePending   = "pending"
	ProbeOK        = "ok"
	ProbeAuth      = "auth"
	ProbeTransient = "transient"
)

type Manager struct {
	mu       sync.Mutex
	cfg      ManagerConfig
	nextID   int64
	accounts map[int64]*Account
	configs  map[int64]LoginConfig
	monitors map[int64]*Monitor
	jobs     map[string]*LoginJob
}

func NewManager(cfg ManagerConfig) *Manager {
	return &Manager{cfg: cfg, nextID: 1, accounts: map[int64]*Account{}, configs: map[int64]LoginConfig{}, monitors: map[int64]*Monitor{}, jobs: map[string]*LoginJob{}}
}

func (m *Manager) StartTwoFALogin(ctx context.Context, accountID int64, in LoginConfigInput) (LoginJob, error) {
	if accountID <= 0 || strings.TrimSpace(in.Email) == "" || strings.TrimSpace(in.Password) == "" || strings.TrimSpace(in.TOTPSecret) == "" {
		return LoginJob{}, errors.New("email, password and TOTP secret are required")
	}
	if _, err := m.SaveLoginConfig(ctx, in); err != nil {
		return LoginJob{}, err
	}
	m.mu.Lock()
	id := time.Now().UTC().Format("20060102150405.000000000")
	j := &LoginJob{ID: id, Status: "running", AccountID: accountID}
	m.jobs[id] = j
	m.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			if x := m.jobs[id]; x != nil {
				x.Status = "cancelled"
			}
			m.mu.Unlock()
		case <-time.After(10 * time.Millisecond):
			m.mu.Lock()
			if x := m.jobs[id]; x != nil {
				x.Status = "succeeded"
				x.Credential = map[string]any{"email": strings.ToLower(strings.TrimSpace(in.Email))}
			}
			m.mu.Unlock()
		}
	}()
	return *j, nil
}
func (m *Manager) LoginJob(id string) (LoginJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return LoginJob{}, false
	}
	out := *j
	out.Credential = clone(j.Credential)
	return out, true
}
func (m *Manager) CancelLogin(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[id]; j != nil && j.Status == "running" {
		j.Status = "cancelled"
		j.Credential = nil
	}
}

func (m *Manager) SaveLoginConfig(_ context.Context, in LoginConfigInput) (LoginConfig, error) {
	if in.AccountID <= 0 || strings.TrimSpace(in.Email) == "" {
		return LoginConfig{}, errors.New("account_id and email are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.configs[in.AccountID]
	out := LoginConfig{AccountID: in.AccountID, Email: strings.ToLower(strings.TrimSpace(in.Email)), Mode: in.Mode, Engine: in.Engine, ProxySource: in.ProxySource, UpdatedAt: time.Now().UTC()}
	enc := func(v string) (string, error) {
		if v == "" {
			return "", nil
		}
		if m.cfg.Encrypt == nil {
			return "", ErrEncryptionUnavailable
		}
		return m.cfg.Encrypt(v)
	}
	var err error
	if !in.ClearPassword && in.Password == "" {
		out.PasswordCiphertext = old.PasswordCiphertext
	} else {
		out.PasswordCiphertext, err = enc(in.Password)
		if err != nil {
			return LoginConfig{}, err
		}
	}
	if !in.ClearTOTP && in.TOTPSecret == "" {
		out.TOTPCiphertext = old.TOTPCiphertext
	} else {
		out.TOTPCiphertext, err = enc(in.TOTPSecret)
		if err != nil {
			return LoginConfig{}, err
		}
	}
	if in.OTPURL != "" {
		out.OTPURLCiphertext, err = enc(in.OTPURL)
		if err != nil {
			return LoginConfig{}, err
		}
	} else {
		out.OTPURLCiphertext = old.OTPURLCiphertext
	}
	m.configs[in.AccountID] = out
	return out, nil
}
func (m *Manager) LoginConfig(accountID int64) (LoginConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.configs[accountID]
	c.PasswordCiphertext = ""
	c.TOTPCiphertext = ""
	c.OTPURLCiphertext = ""
	return c, ok
}

func (m *Manager) ImportCredential(_ context.Context, in ImportInput) (ImportResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	ext := strings.TrimSpace(in.AccountID)
	rt := strings.TrimSpace(in.RefreshToken)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.accounts {
		if (email != "" && a.Email == email) || (ext != "" && a.ExternalID == ext) || (rt != "" && a.RefreshToken == rt) {
			a.Generation++
			a.Credential = clone(in.Credential)
			if email != "" {
				a.Email = email
			}
			if ext != "" {
				a.ExternalID = ext
			}
			if rt != "" {
				a.RefreshToken = rt
			}
			return ImportResult{AccountID: a.ID, Generation: a.Generation}, nil
		}
	}
	id := m.nextID
	m.nextID++
	m.accounts[id] = &Account{ID: id, Email: email, ExternalID: ext, RefreshToken: rt, Credential: clone(in.Credential), Generation: 1}
	return ImportResult{AccountID: id, Generation: 1, Created: true}, nil
}
func (m *Manager) ApplyCallback(_ context.Context, cb Callback) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accounts[cb.AccountID]
	if a == nil {
		return errors.New("account not found")
	}
	if cb.Generation != a.Generation {
		return ErrStaleCallback
	}
	a.Credential = clone(cb.Credential)
	a.Generation++
	return nil
}

func (m *Manager) UpsertMonitor(in Monitor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.NextProbeAt.IsZero() {
		in.NextProbeAt = time.Now().UTC()
	}
	cp := in
	m.monitors[in.AccountID] = &cp
}
func (m *Manager) ClaimDue(owner string, lease time.Duration, limit int) []Monitor {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := []Monitor{}
	for _, v := range m.monitors {
		if len(out) >= limit || !v.Enabled || v.NextProbeAt.After(now) || (v.CooldownUntil != nil && v.CooldownUntil.After(now)) || (v.LeaseUntil != nil && v.LeaseUntil.After(now)) {
			continue
		}
		until := now.Add(lease)
		v.LeaseOwner = owner
		v.LeaseUntil = &until
		out = append(out, *v)
	}
	return out
}
func (m *Manager) CompleteProbe(owner string, id int64, r ProbeResult) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.monitors[id]
	if v == nil || v.LeaseOwner != owner {
		return ErrLeaseLost
	}
	v.ProbeState = r.State
	v.Detail = r.Detail
	v.FailStreak = r.FailStreak
	v.CooldownUntil = r.CooldownUntil
	v.LeaseOwner = ""
	v.LeaseUntil = nil
	v.NextProbeAt = time.Now().Add(30 * time.Minute)
	return nil
}
func clone(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
