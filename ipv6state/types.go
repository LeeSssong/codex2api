package ipv6state

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/codex2api/statepool"
)

const Header = statepool.Header
const Lifetime = time.Hour
const RefreshBefore = 10 * time.Minute

var ErrStateRequired = errors.New("valid_state_required")

type Config struct {
	RefreshBeforeMinutes      int      `json:"refresh_before_minutes"`
	StagedConcurrency         bool     `json:"staged_concurrency"`
	UrgentBeforeMinutes       int      `json:"urgent_before_minutes"`
	EarlyConcurrency          int      `json:"early_concurrency"`
	UrgentConcurrency         int      `json:"urgent_concurrency"`
	ExpiredConcurrency        int      `json:"expired_concurrency"`
	UrgentBusinessConcurrency int      `json:"urgent_business_concurrency"`
	Enabled                   bool     `json:"enabled"`
	CaptureMode               string   `json:"capture_mode"`
	ProxyIDs                  []int64  `json:"proxy_ids"`
	ForwardProxyID            int64    `json:"forward_proxy_id"`
	NewSession                bool     `json:"new_session"`
	AccountIDs                []int64  `json:"account_ids"`
	Models                    []string `json:"models"`
	SourceIPs                 []string `json:"source_ips"`
	IntervalSeconds           int      `json:"interval_seconds"`
	AcceptedLengths           []int    `json:"accepted_lengths"`
	Concurrency               int      `json:"concurrency"`
	RequireValidState         bool     `json:"require_valid_state"`
}

func DefaultConfig() Config {
	return Config{RefreshBeforeMinutes: 10, UrgentBeforeMinutes: 10, EarlyConcurrency: 1, UrgentConcurrency: 3, ExpiredConcurrency: 5, UrgentBusinessConcurrency: 1, CaptureMode: "proxy", ProxyIDs: []int64{}, NewSession: true, AccountIDs: []int64{}, Models: slices.Clone(statepool.Models), SourceIPs: []string{}, IntervalSeconds: 3, AcceptedLengths: []int{292}, Concurrency: 20}
}

func (c Config) Validate() error {
	if c.RefreshBeforeMinutes < 1 || c.RefreshBeforeMinutes > 59 || c.UrgentBeforeMinutes < 1 || c.UrgentBeforeMinutes > c.RefreshBeforeMinutes {
		return errors.New("invalid_refresh_window")
	}
	for _, limit := range []int{c.EarlyConcurrency, c.UrgentConcurrency, c.ExpiredConcurrency, c.UrgentBusinessConcurrency} {
		if limit < 1 || limit > 20 {
			return errors.New("invalid_stage_concurrency")
		}
	}
	if c.Concurrency < 1 || c.Concurrency > 20 {
		return errors.New("capture concurrency must be between 1 and 20")
	}
	if len(c.AcceptedLengths) == 0 || len(c.AcceptedLengths) > 32 {
		return errors.New("select between 1 and 32 accepted state lengths")
	}
	lengths := map[int]bool{}
	for _, length := range c.AcceptedLengths {
		if length < 1 || length > 8192 || lengths[length] {
			return errors.New("state lengths must be distinct integers between 1 and 8192")
		}
		lengths[length] = true
	}
	if c.CaptureMode != "proxy" && c.CaptureMode != "local_ipv6" && c.CaptureMode != "mixed" || len(c.ProxyIDs) > 128 || c.ForwardProxyID < 0 {
		return errors.New("invalid capture mode or proxy selection")
	}
	proxyIDs := map[int64]bool{}
	for _, id := range c.ProxyIDs {
		if id <= 0 || proxyIDs[id] || id == c.ForwardProxyID {
			return errors.New("invalid, duplicate, or circular capture proxy")
		}
		proxyIDs[id] = true
	}
	if len(c.Models) == 0 || len(c.Models) > 32 || len(c.AccountIDs) > 1024 || len(c.SourceIPs) > 4096 || c.IntervalSeconds < 1 || c.IntervalSeconds > 300 {
		return errors.New("invalid IPv6 state configuration")
	}
	seen := map[string]bool{}
	for _, model := range c.Models {
		canonical, err := statepool.NormalizeModel(model)
		if err != nil || canonical != model || seen[model] {
			return errors.New("select distinct exact models")
		}
		seen[model] = true
	}
	ids := map[int64]bool{}
	for _, id := range c.AccountIDs {
		if id <= 0 || ids[id] {
			return errors.New("invalid or duplicate account ID")
		}
		ids[id] = true
	}
	seen = map[string]bool{}
	for _, value := range c.SourceIPs {
		ip, err := netip.ParseAddr(value)
		if err != nil || !publicIPv6(ip) || value != ip.String() || seen[value] {
			return errors.New("source addresses must be distinct public IPv6 literals")
		}
		seen[value] = true
	}
	return nil
}

func (c Config) refreshBefore() time.Duration {
	return time.Duration(c.RefreshBeforeMinutes) * time.Minute
}

func (c Config) accepts(value string) bool {
	return slices.Contains(c.AcceptedLengths, len(value))
}

func publicIPv6(ip netip.Addr) bool {
	return ip.Is6() && !ip.Is4In6() && ip.Zone() == "" && ip.IsGlobalUnicast() && !ip.IsPrivate()
}

func LocalIPv6() ([]string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	result := []string{}
	for _, address := range addresses {
		prefix, err := netip.ParsePrefix(address.String())
		if err == nil && publicIPv6(prefix.Addr()) && !slices.Contains(result, prefix.Addr().String()) {
			result = append(result, prefix.Addr().String())
		}
	}
	sort.Strings(result)
	return result, nil
}

// The Fernet timestamp is an issue time, not an expiry or an authenticated claim.
// Only the upstream can verify the MAC; one hour is our local retention policy.
func TokenTimes(token string, now time.Time) (int64, int64, error) {
	if len(token) > 8192 || strings.TrimSpace(token) != token || strings.ContainsAny(token, "\r\n") {
		return 0, 0, errors.New("invalid_token_format")
	}
	raw, err := base64.URLEncoding.Strict().DecodeString(token)
	if err != nil {
		raw, err = base64.RawURLEncoding.Strict().DecodeString(token)
	}
	if err != nil || len(raw) < 73 || raw[0] != 0x80 || (len(raw)-57)%16 != 0 {
		return 0, 0, errors.New("invalid_token_format")
	}
	issued := binary.BigEndian.Uint64(raw[1:9])
	if issued == 0 || issued > uint64(now.Unix()) {
		return 0, 0, errors.New("invalid_token_timestamp")
	}
	expires := int64(issued) + int64(Lifetime.Seconds())
	if expires <= now.Unix() {
		return 0, 0, errors.New("token_expired")
	}
	return int64(issued), expires, nil
}

type Entry struct {
	CaptureStage   string             `json:"capture_stage"`
	Valid          bool               `json:"valid"`
	Available      bool               `json:"available"`
	CapturePhase   string             `json:"capture_phase"`
	UpdatedAt      int64              `json:"updated_at,omitempty"`
	Refreshing     bool               `json:"refreshing"`
	CooldownReason string             `json:"cooldown_reason,omitempty"`
	CooldownUntil  int64              `json:"cooldown_until,omitempty"`
	RetrySource    string             `json:"retry_source,omitempty"`
	AccountID      int64              `json:"account_id"`
	AccountName    string             `json:"account_name"`
	Model          string             `json:"model"`
	Identity       statepool.Identity `json:"identity"`
	Value          string             `json:"-"`
	Fingerprint    string             `json:"fingerprint,omitempty"`
	IssuedAt       int64              `json:"issued_at"`
	ExpiresAt      int64              `json:"expires_at"`
	CapturedAt     int64              `json:"captured_at"`
	SourceIP       string             `json:"source_ip,omitempty"`
	ProxyID        int64              `json:"proxy_id,omitempty"`
	ProxyName      string             `json:"proxy_name,omitempty"`
	SessionID      string             `json:"session_id,omitempty"`
	Attempts       int64              `json:"attempts"`
	LastLength     int                `json:"last_length"`
	HTTPStatus     int                `json:"http_status"`
	Status         string             `json:"status"`
	Error          string             `json:"error,omitempty"`
	RetryAt        int64              `json:"retry_at"`
}

// Proxy credentials only travel to the executor, never to status or migration.
type Route struct {
	SourceIP   string
	ProxyID    int64
	ProxyName  string
	SessionID  string
	ProxyURL   string `json:"-"`
	ForwardURL string `json:"-"`
}

type Portable struct {
	Format        string `json:"format"`
	MemberHash    string `json:"member_hash"`
	WorkspaceHash string `json:"workspace_hash"`
	Model         string `json:"model"`
	Value         string `json:"value"`
}

type Status struct {
	Summary            Summary  `json:"summary"`
	Config             Config   `json:"config"`
	Entries            []Entry  `json:"entries"`
	LocalIPs           []string `json:"local_ips"`
	Running            bool     `json:"running"`
	ActiveRequests     int      `json:"active_requests"`
	AccountConcurrency int      `json:"account_concurrency"`
	Error              string   `json:"error,omitempty"`
	ServerTime         int64    `json:"server_time"`
}
