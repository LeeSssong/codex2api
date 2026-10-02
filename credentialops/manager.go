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
	configs  map[int64]LoginConfig
	monitors map[int64]*Monitor
}

func NewManager(cfg ManagerConfig) *Manager {
	return &Manager{cfg: cfg, configs: map[int64]LoginConfig{}, monitors: map[int64]*Monitor{}}
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
