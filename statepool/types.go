package statepool

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codex2api/auth"
)

const Header = "X-Codex-Turn-State"
const Lifetime = time.Hour
const Benchmark = "candy-shape-and-holdout-v1"

var Models = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-6-astra"}

type Identity struct {
	MemberHash           string `json:"member_hash"`
	WorkspaceHash        string `json:"workspace_hash"`
	CredentialHash       string `json:"credential_hash"`
	CredentialGeneration int64  `json:"credential_generation"`
	ProxyHash            string `json:"proxy_hash"`
}

type Check struct {
	Phase            string `json:"phase"`
	HTTPStatus       int    `json:"http_status"`
	Terminal         string `json:"terminal,omitempty"`
	Model            string `json:"response_model,omitempty"`
	Passed           bool   `json:"passed"`
	Answer           string `json:"answer,omitempty"`
	Error            string `json:"error,omitempty"`
	DurationMS       int64  `json:"duration_ms"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	RetryAt          int64  `json:"retry_at,omitempty"`
	ProxyID          int64  `json:"proxy_id,omitempty"`
	ProxyName        string `json:"proxy_name,omitempty"`
	LastTestIP       string `json:"last_test_ip,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	ForwardProxyName string `json:"forward_proxy_name,omitempty"`
}

type Entry struct {
	ID          string   `json:"id"`
	AccountID   int64    `json:"account_id"`
	AccountName string   `json:"account_name"`
	Model       string   `json:"model"`
	Effort      string   `json:"effort"`
	Identity    Identity `json:"identity"`
	Value       string   `json:"-"`
	Fingerprint string   `json:"fingerprint"`
	CapturedAt  int64    `json:"captured_at"`
	ExpiresAt   int64    `json:"expires_at"`
	VerifiedAt  int64    `json:"verified_at"`
	Enabled     bool     `json:"enabled"`
	Strict      bool     `json:"strict"`
	Source      string   `json:"source"`
	Benchmark   string   `json:"benchmark"`
	Checks      []Check  `json:"checks"`
	Status      string   `json:"status"`
}

type Job struct {
	ID           string   `json:"id"`
	BatchID      string   `json:"batch_id"`
	AccountID    int64    `json:"account_id"`
	AccountName  string   `json:"account_name"`
	Model        string   `json:"model"`
	Effort       string   `json:"effort"`
	Identity     Identity `json:"identity"`
	Status       string   `json:"status"`
	Phase        string   `json:"phase"`
	Error        string   `json:"error,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
	CapturedAt   int64    `json:"captured_at,omitempty"`
	ExpiresAt    int64    `json:"expires_at,omitempty"`
	RetryAt      int64    `json:"retry_at,omitempty"`
	Enable       bool     `json:"enable"`
	Strict       bool     `json:"strict"`
	Source       string   `json:"source"`
	EntryID      string   `json:"entry_id,omitempty"`
	Checks       []Check  `json:"checks"`
	GroupID      string   `json:"group_id,omitempty"`
	Candidate    int      `json:"candidate,omitempty"`
	Candidates   int      `json:"candidates,omitempty"`
	Strategy     string   `json:"strategy,omitempty"`
	CaptureProxy ProxyRef `json:"capture_proxy"`
	ForwardProxy ProxyRef `json:"forward_proxy"`
}

type ProxyRef struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	LastTestIP    string `json:"last_test_ip,omitempty"`
	URLHash       string `json:"url_hash,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	RotateSession bool   `json:"rotate_session,omitempty"`
	Dynamic       bool   `json:"dynamic,omitempty"`
}

type CaptureOptions struct {
	ProxyIDs       []int64 `json:"proxy_ids"`
	Candidates     int     `json:"candidates"`
	Strategy       string  `json:"strategy"`
	DistinctIPs    bool    `json:"distinct_ips"`
	NewSession     bool    `json:"new_session"`
	ForwardProxyID int64   `json:"forward_proxy_id,omitempty"`
}

// Packages contain no OAuth credentials or machine-local account IDs.
type PortableState struct {
	MemberHash    string  `json:"member_hash"`
	WorkspaceHash string  `json:"workspace_hash"`
	Model         string  `json:"model"`
	Effort        string  `json:"effort"`
	Value         string  `json:"value"`
	Fingerprint   string  `json:"fingerprint"`
	CapturedAt    int64   `json:"captured_at"`
	ExpiresAt     int64   `json:"expires_at"`
	Benchmark     string  `json:"benchmark"`
	Checks        []Check `json:"checks,omitempty"`
}

type Package struct {
	Format     string          `json:"format"`
	Version    int             `json:"version"`
	ExportedAt int64           `json:"exported_at"`
	States     []PortableState `json:"states"`
}

func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func Snapshot(account *auth.Account, proxyURL string) (Identity, error) {
	if account == nil || account.IsRelayStyle() || account.IsCodexAgentIdentity() {
		return Identity{}, errors.New("an OAuth Codex account is required")
	}
	for name, value := range account.GetCustomHeaders() {
		if value != "" && (strings.EqualFold(name, Header) || strings.EqualFold(name, "Authorization")) {
			return Identity{}, errors.New("remove custom Authorization/turn-state headers before using the state pool")
		}
	}
	account.Mu().RLock()
	token, generation := account.AccessToken, account.CredentialGeneration
	account.Mu().RUnlock()
	workspace := account.EffectiveAccountID()
	parts := strings.Split(token, ".")
	if len(parts) != 3 || workspace == "" {
		return Identity{}, errors.New("missing account identity")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, errors.New("invalid credential identity")
	}
	var claims struct {
		Sub  string `json:"sub"`
		Auth struct {
			UserID string `json:"chatgpt_user_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return Identity{}, errors.New("invalid credential identity")
	}
	member := claims.Auth.UserID
	if member == "" {
		member = claims.Sub
	}
	if member == "" {
		return Identity{}, errors.New("missing account member identity")
	}
	return Identity{MemberHash: Hash(member), WorkspaceHash: Hash(workspace),
		CredentialHash: Hash(token + "\x00" + workspace), CredentialGeneration: generation, ProxyHash: Hash(proxyURL)}, nil
}

func NormalizeModel(value string) (string, error) {
	aliases := map[string]string{"sol": Models[0], "terra": Models[1], "luna": Models[2], "6astra": Models[3], "astra": Models[3]}
	value = strings.ToLower(strings.TrimSpace(value))
	if alias, ok := aliases[value]; ok {
		value = alias
	}
	for _, model := range Models {
		if value == model {
			return value, nil
		}
	}
	return "", fmt.Errorf("unsupported state model: %s", value)
}

func ValidatePortable(state PortableState, now time.Time) error {
	model, err := NormalizeModel(state.Model)
	if err != nil || model != state.Model || state.Effort != "high" {
		return errors.New("package model or reasoning effort is unsupported")
	}
	if len(state.Value) == 0 || len(state.Value) > 16384 || strings.TrimSpace(state.Value) != state.Value {
		return errors.New("invalid state value length or surrounding whitespace")
	}
	for _, c := range state.Value {
		if c < 0x21 || c > 0x7e {
			return errors.New("state contains invalid header characters")
		}
	}
	if state.CapturedAt <= 0 || state.CapturedAt > now.Unix() || state.ExpiresAt <= now.Unix() ||
		state.ExpiresAt <= state.CapturedAt || state.ExpiresAt-state.CapturedAt > int64(Lifetime.Seconds()) {
		return errors.New("state expired or invalid original capture/expiry time")
	}
	if len(state.MemberHash) != 64 || len(state.WorkspaceHash) != 64 || state.Fingerprint != Hash(state.Value) {
		return errors.New("state identity or fingerprint is invalid")
	}
	return nil
}
