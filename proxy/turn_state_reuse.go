package proxy

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/cache"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/turnstate"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const turnStateReuseConfigCacheTTL = 3 * time.Second

type turnStateReuseSnapshot struct {
	settings      database.TurnStateReuseSettings
	enabledGroups map[int64]struct{}
	loadedAt      time.Time
}

type turnStateReuseRuntime struct {
	db    *database.DB
	store *turnstate.Store
	mu    sync.RWMutex
	snap  turnStateReuseSnapshot
}

type TurnStateReuseAccountStatus struct {
	AccountID        int64  `json:"account_id"`
	AccountName      string `json:"account_name,omitempty"`
	Status           string `json:"status"`
	EncodedLength    int    `json:"encoded_length,omitempty"`
	DecodedLength    int    `json:"decoded_length,omitempty"`
	IssuedAt         string `json:"issued_at,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	RemainingSeconds int64  `json:"remaining_seconds,omitempty"`
	LastHTTPStatus   int    `json:"last_http_status,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	LastRoute        string `json:"last_route,omitempty"`
	MissSuspended    bool   `json:"turn_state_miss_suspended"`
}

var activeTurnStateReuseRuntime atomic.Pointer[turnStateReuseRuntime]

func newTurnStateReuseRuntime(db *database.DB, backend cache.TokenCache) *turnStateReuseRuntime {
	return &turnStateReuseRuntime{db: db, store: turnstate.NewStore(backend)}
}

func configureTurnStateReuseRuntime(db *database.DB, backend cache.TokenCache) {
	if db == nil || backend == nil {
		activeTurnStateReuseRuntime.Store(nil)
		return
	}
	activeTurnStateReuseRuntime.Store(newTurnStateReuseRuntime(db, backend))
}

// InvalidateTurnStateReuseRuntime forces the next request to reload settings and
// enabled-group membership after an admin update.
func InvalidateTurnStateReuseRuntime() {
	if runtime := activeTurnStateReuseRuntime.Load(); runtime != nil {
		runtime.invalidate()
	}
}

func (r *turnStateReuseRuntime) invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.snap.loadedAt = time.Time{}
	r.mu.Unlock()
}

func (r *turnStateReuseRuntime) snapshot(ctx context.Context, now time.Time) (turnStateReuseSnapshot, bool) {
	if r == nil || r.db == nil || r.store == nil {
		return turnStateReuseSnapshot{}, false
	}
	r.mu.RLock()
	snap := r.snap
	r.mu.RUnlock()
	if !snap.loadedAt.IsZero() && now.Sub(snap.loadedAt) < turnStateReuseConfigCacheTTL {
		return snap, true
	}

	settings, err := r.db.GetTurnStateReuseSettings(ctx)
	if err != nil {
		return turnStateReuseSnapshot{}, false
	}
	groups, err := r.db.ListAccountGroups(ctx)
	if err != nil {
		return turnStateReuseSnapshot{}, false
	}
	enabledGroups := make(map[int64]struct{})
	for _, group := range groups {
		if group.TurnStateInjectEnabled && database.NormalizeAccountGroupChannel(group.Channel) == database.AccountGroupChannelCodex {
			enabledGroups[group.ID] = struct{}{}
		}
	}
	snap = turnStateReuseSnapshot{settings: settings, enabledGroups: enabledGroups, loadedAt: now}
	r.mu.Lock()
	r.snap = snap
	r.mu.Unlock()
	return snap, true
}

func turnStateAccountIdentity(account *auth.Account) (accessToken, accountID string, groupIDs []int64, ok bool) {
	if account == nil || account.IsRelayStyle() || account.IsCodexAgentIdentity() {
		return "", "", nil, false
	}
	account.Mu().RLock()
	accessToken = strings.TrimSpace(account.AccessToken)
	accountID = strings.TrimSpace(account.AccountID)
	groupIDs = append([]int64(nil), account.GroupIDs...)
	account.Mu().RUnlock()
	return accessToken, accountID, groupIDs, accessToken != "" && accountID != ""
}

func accountInTurnStateScope(groupIDs []int64, enabledGroups map[int64]struct{}) bool {
	for _, groupID := range groupIDs {
		if _, ok := enabledGroups[groupID]; ok {
			return true
		}
	}
	return false
}

func bodyHasCompactionTrigger(body []byte) bool {
	input := gjson.GetBytes(body, "input")
	if input.IsObject() {
		return strings.EqualFold(strings.TrimSpace(input.Get("type").String()), "compaction_trigger")
	}
	found := false
	input.ForEach(func(_, value gjson.Result) bool {
		if value.IsObject() && strings.EqualFold(strings.TrimSpace(value.Get("type").String()), "compaction_trigger") {
			found = true
			return false
		}
		return true
	})
	return found
}

func turnStateRequestEligible(body []byte, endpoint string) bool {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String()) == turnstate.HarvestModel &&
		strings.TrimRight(strings.TrimSpace(endpoint), "/") == "/responses" &&
		!bodyHasCompactionTrigger(body)
}

func (r *turnStateReuseRuntime) ticketForAccount(ctx context.Context, account *auth.Account, model string, now time.Time) (turnstate.Ticket, bool, error) {
	accessToken, accountID, _, ok := turnStateAccountIdentity(account)
	if !ok {
		return turnstate.Ticket{}, false, nil
	}
	key := turnstate.Key{AccountID: account.ID(), Model: model, CredentialHash: turnstate.CredentialHash(accessToken, accountID)}
	return r.store.Get(ctx, key, now)
}

func (r *turnStateReuseRuntime) resolve(ctx context.Context, account *auth.Account, body []byte, endpoint string) (turnstate.Ticket, bool) {
	now := time.Now()
	snap, ok := r.snapshot(ctx, now)
	if !ok || !snap.settings.Enabled {
		return turnstate.Ticket{}, false
	}
	_, _, groupIDs, ok := turnStateAccountIdentity(account)
	if !ok || !accountInTurnStateScope(groupIDs, snap.enabledGroups) {
		return turnstate.Ticket{}, false
	}
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	ticket, found, err := r.ticketForAccount(ctx, account, model, now)
	if err != nil || !found {
		return turnstate.Ticket{}, false
	}
	decision := turnstate.DecisionInput{
		Enabled:              true,
		InScope:              true,
		Endpoint:             endpoint,
		Model:                model,
		HasCompactionTrigger: bodyHasCompactionTrigger(body),
		Ticket:               &ticket,
		Now:                  now,
	}
	headers := make(http.Header)
	if !turnstate.ApplyOutbound(headers, decision) {
		return turnstate.Ticket{}, false
	}
	return ticket, true
}

func (r *turnStateReuseRuntime) schedulerFilter(ctx context.Context, body []byte, endpoint string, inner auth.AccountFilter) auth.AccountFilter {
	return func(account *auth.Account) bool {
		if inner != nil && !inner(account) {
			return false
		}
		now := time.Now()
		snap, ok := r.snapshot(ctx, now)
		if !ok || !snap.settings.Enabled || !turnStateRequestEligible(body, endpoint) {
			return true
		}
		_, _, groupIDs, identityOK := turnStateAccountIdentity(account)
		if !identityOK || !accountInTurnStateScope(groupIDs, snap.enabledGroups) {
			return true
		}
		if snap.settings.MissAction == database.TurnStateMissNone {
			return true
		}
		_, found, err := r.ticketForAccount(ctx, account, turnstate.HarvestModel, now)
		return err == nil && found
	}
}

func (r *turnStateReuseRuntime) accountStatus(ctx context.Context, account *auth.Account, now time.Time) TurnStateReuseAccountStatus {
	status := TurnStateReuseAccountStatus{Status: "out_of_scope"}
	if account == nil {
		return status
	}
	status.AccountID = account.ID()
	account.Mu().RLock()
	status.MissSuspended = account.CooldownReason == "turn_state_miss"
	account.Mu().RUnlock()
	snap, ok := r.snapshot(ctx, now)
	if !ok || !snap.settings.Enabled {
		return status
	}
	_, _, groupIDs, identityOK := turnStateAccountIdentity(account)
	if !identityOK || !accountInTurnStateScope(groupIDs, snap.enabledGroups) {
		return status
	}
	status.Status = "missing"
	ticket, found, err := r.ticketForAccount(ctx, account, turnstate.HarvestModel, now)
	if err != nil || !found {
		return status
	}
	status.Status = "fresh"
	if ticket.RenewalDue(now) {
		status.Status = "renew_due"
	}
	status.EncodedLength = len(ticket.Raw)
	status.DecodedLength = ticket.DecodedLength
	status.IssuedAt = ticket.IssuedAt.UTC().Format(time.RFC3339)
	status.ExpiresAt = ticket.ExpiresAt.UTC().Format(time.RFC3339)
	status.RemainingSeconds = max(0, int64(ticket.ExpiresAt.Sub(now).Seconds()))
	return status
}

func withTurnStateReuseSchedulerFilter(ctx context.Context, body []byte, endpoint string, inner auth.AccountFilter) auth.AccountFilter {
	runtime := activeTurnStateReuseRuntime.Load()
	if runtime == nil {
		return inner
	}
	return runtime.schedulerFilter(ctx, body, endpoint, inner)
}

func TurnStateReuseStatuses(ctx context.Context, accounts []*auth.Account) []TurnStateReuseAccountStatus {
	runtime := activeTurnStateReuseRuntime.Load()
	if runtime == nil {
		return []TurnStateReuseAccountStatus{}
	}
	now := time.Now()
	result := make([]TurnStateReuseAccountStatus, 0, len(accounts))
	for _, account := range accounts {
		status := runtime.accountStatus(ctx, account, now)
		if status.Status != "out_of_scope" {
			result = append(result, status)
		}
	}
	return result
}

func (r *turnStateReuseRuntime) applyHTTP(ctx context.Context, headers http.Header, account *auth.Account, body []byte, endpoint string) bool {
	if headers == nil {
		return false
	}
	ticket, ok := r.resolve(ctx, account, body, endpoint)
	if !ok {
		return false
	}
	headers.Set(turnstate.HeaderName, ticket.Raw)
	return true
}

func (r *turnStateReuseRuntime) applyWebsocket(ctx context.Context, account *auth.Account, body []byte) ([]byte, bool) {
	ticket, ok := r.resolve(ctx, account, body, "/responses")
	if !ok {
		return body, false
	}
	updated, err := sjson.SetBytes(body, "client_metadata.x-codex-turn-state", ticket.Raw)
	if err != nil {
		return body, false
	}
	return updated, true
}

func applyTurnStateReuseHTTP(ctx context.Context, headers http.Header, account *auth.Account, body []byte, endpoint string) bool {
	runtime := activeTurnStateReuseRuntime.Load()
	return runtime != nil && runtime.applyHTTP(ctx, headers, account, body, endpoint)
}

// ApplyTurnStateReuseWebsocket overlays the current account ticket on an
// already-normalized response.create body. Failures return the original bytes.
func ApplyTurnStateReuseWebsocket(ctx context.Context, account *auth.Account, body []byte) []byte {
	runtime := activeTurnStateReuseRuntime.Load()
	if runtime == nil {
		return body
	}
	updated, _ := runtime.applyWebsocket(ctx, account, body)
	return updated
}
