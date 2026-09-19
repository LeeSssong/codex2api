package admin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/turnstate"
	"github.com/codex2api/proxy"
	"github.com/tidwall/gjson"
)

const (
	turnStateMissingCadence = 20 * time.Second
	turnStateRenewCadence   = 5 * time.Minute
	turnStateLeaseTTL       = 90 * time.Second
	turnStateWorkerTick     = time.Second
	turnStateMaxConcurrency = 3
	turnStateMissReason     = "turn_state_miss"
	turnStateLeaseNamespace = "codex_turn_state_harvest"
)

type turnStateHarvestResult struct {
	Ticket     turnstate.Ticket
	StatusCode int
	RetryAfter time.Duration
	AuthFailed bool
}

type turnStateHarvestAccountState struct {
	LastAttempt    time.Time
	BackoffUntil   time.Time
	AuthPaused     bool
	MissApplied    bool
	Status         string
	LastHTTPStatus int
	LastError      string
	LastRoute      string
}

type turnStateHarvestTarget struct {
	account *auth.Account
	key     turnstate.Key
	stateID string
}

// StartTurnStateHarvester starts the account-scoped background collector. The
// database lifecycle waits for it and parent cancellation stops new work.
func (h *Handler) StartTurnStateHarvester(parent context.Context) {
	if h == nil || h.db == nil || h.store == nil || h.cache == nil {
		return
	}
	h.turnStateHarvestMu.Lock()
	if h.turnStateHarvestStates != nil {
		h.turnStateHarvestMu.Unlock()
		return
	}
	h.turnStateHarvestStates = make(map[string]*turnStateHarvestAccountState)
	h.turnStateHarvestOwner = newTurnStateHarvestOwner()
	h.turnStateHarvestMu.Unlock()
	h.startDBBackgroundTaskWithParent(parent, h.runTurnStateHarvester)
}

func newTurnStateHarvestOwner() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err == nil {
		return hex.EncodeToString(raw)
	}
	return fmt.Sprintf("turn-state-%d", time.Now().UnixNano())
}

func (h *Handler) runTurnStateHarvester(ctx context.Context) {
	ticker := time.NewTicker(turnStateWorkerTick)
	defer ticker.Stop()
	sem := make(chan struct{}, turnStateMaxConcurrency)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	for {
		h.sweepTurnStateHarvester(ctx, sem, &jobs, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handler) sweepTurnStateHarvester(ctx context.Context, sem chan struct{}, jobs *sync.WaitGroup, now time.Time) {
	settings, err := h.db.GetTurnStateReuseSettings(ctx)
	if err != nil {
		return
	}
	accounts := h.store.Accounts()
	if !settings.Enabled {
		for _, account := range accounts {
			_, _ = h.store.ClearCooldownIfReason(ctx, account, turnStateMissReason)
		}
		return
	}
	enabledGroups, err := h.turnStateEnabledGroups(ctx)
	if err != nil {
		return
	}
	routes := h.turnStateHarvestRoutes(ctx, settings)
	ticketStore := turnstate.NewStore(h.cache)
	for _, account := range accounts {
		target, ok := turnStateTargetForAccount(account, enabledGroups)
		if !ok {
			continue
		}
		state := h.ensureTurnStateHarvestState(target.stateID)
		ticket, found, getErr := ticketStore.Get(ctx, target.key, now)
		if getErr != nil {
			found = false
		}
		if found && !ticket.RenewalDue(now) {
			h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) { st.Status = "fresh" })
			continue
		}
		cadence := turnStateMissingCadence
		if found {
			cadence = turnStateRenewCadence
		}
		if state.AuthPaused || now.Before(state.BackoffUntil) || (!state.LastAttempt.IsZero() && now.Sub(state.LastAttempt) < cadence) {
			continue
		}
		if len(routes) == 0 {
			h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) {
				st.LastAttempt, st.LastError, st.Status = now, "no_harvest_route", "missing"
			})
			if !found && !state.MissApplied {
				h.applyTurnStateMissAction(ctx, account, settings, now)
				h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) { st.MissApplied = true })
			}
			continue
		}
		leaseKey := "account:" + target.key.String()
		leased, leaseErr := h.cache.AcquireLease(ctx, turnStateLeaseNamespace, leaseKey, h.turnStateHarvestOwner, turnStateLeaseTTL)
		if leaseErr != nil || !leased {
			continue
		}
		route := routes[int(h.turnStateHarvestRoute.Add(1)-1)%len(routes)]
		h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) {
			st.LastAttempt, st.LastRoute = now, redactTurnStateRoute(route)
		})
		select {
		case sem <- struct{}{}:
			jobs.Add(1)
			go func(target turnStateHarvestTarget, hadTicket bool, route, leaseKey string) {
				defer jobs.Done()
				defer func() { <-sem }()
				defer h.cache.ReleaseLease(context.Background(), turnStateLeaseNamespace, leaseKey, h.turnStateHarvestOwner)
				h.harvestTurnStateAccount(ctx, ticketStore, target, hadTicket, route, settings, now)
			}(target, found, route, leaseKey)
		case <-ctx.Done():
			_ = h.cache.ReleaseLease(context.Background(), turnStateLeaseNamespace, leaseKey, h.turnStateHarvestOwner)
			return
		default:
			_ = h.cache.ReleaseLease(context.Background(), turnStateLeaseNamespace, leaseKey, h.turnStateHarvestOwner)
		}
	}
}

func (h *Handler) turnStateEnabledGroups(ctx context.Context) (map[int64]struct{}, error) {
	groups, err := h.db.ListAccountGroups(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]struct{})
	for _, group := range groups {
		if group.TurnStateInjectEnabled && database.NormalizeAccountGroupChannel(group.Channel) == database.AccountGroupChannelCodex {
			result[group.ID] = struct{}{}
		}
	}
	return result, nil
}

func turnStateTargetForAccount(account *auth.Account, enabledGroups map[int64]struct{}) (turnStateHarvestTarget, bool) {
	if account == nil || account.IsRelayStyle() || account.IsCodexAgentIdentity() {
		return turnStateHarvestTarget{}, false
	}
	account.Mu().RLock()
	accessToken := strings.TrimSpace(account.AccessToken)
	accountID := strings.TrimSpace(account.AccountID)
	groupIDs := append([]int64(nil), account.GroupIDs...)
	account.Mu().RUnlock()
	if accessToken == "" || accountID == "" {
		return turnStateHarvestTarget{}, false
	}
	inScope := false
	for _, groupID := range groupIDs {
		if _, ok := enabledGroups[groupID]; ok {
			inScope = true
			break
		}
	}
	if !inScope {
		return turnStateHarvestTarget{}, false
	}
	key := turnstate.Key{AccountID: account.ID(), Model: turnstate.HarvestModel, CredentialHash: turnstate.CredentialHash(accessToken, accountID)}
	return turnStateHarvestTarget{account: account, key: key, stateID: key.String()}, true
}

func (h *Handler) turnStateHarvestRoutes(ctx context.Context, settings database.TurnStateReuseSettings) []string {
	routes := make([]string, 0, len(settings.HarvestProxyURLs))
	for _, route := range settings.HarvestProxyURLs {
		if route = strings.TrimSpace(route); route != "" {
			routes = append(routes, route)
		}
	}
	if len(routes) > 0 || !settings.HarvestUseProxyPool {
		return routes
	}
	rows, err := h.db.ListEnabledProxies(ctx)
	if err != nil {
		return routes
	}
	for _, row := range rows {
		if row != nil && strings.TrimSpace(row.URL) != "" {
			routes = append(routes, strings.TrimSpace(row.URL))
		}
	}
	return routes
}

func (h *Handler) harvestTurnStateAccount(ctx context.Context, store *turnstate.Store, target turnStateHarvestTarget, hadTicket bool, route string, settings database.TurnStateReuseSettings, now time.Time) {
	result, err := h.harvestTurnStateTicket(ctx, target.account, route, now)
	h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) {
		st.LastHTTPStatus = result.StatusCode
		st.LastError = ""
		if err != nil {
			st.LastError = "harvest_failed"
		}
	})
	if result.StatusCode == http.StatusTooManyRequests {
		backoff := result.RetryAfter
		if backoff < turnStateRenewCadence {
			backoff = turnStateRenewCadence
		}
		h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) {
			st.BackoffUntil, st.Status = now.Add(backoff), "paused_429"
		})
		return
	}
	if result.AuthFailed {
		h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) {
			st.AuthPaused, st.Status = true, "paused_auth"
		})
		if !hadTicket && !h.turnStateMissApplied(target.stateID) {
			h.applyTurnStateMissAction(ctx, target.account, settings, now)
			h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) { st.MissApplied = true })
		}
		return
	}
	if err != nil || result.StatusCode != http.StatusOK || !result.Ticket.Valid(now) {
		if !hadTicket && !h.turnStateMissApplied(target.stateID) {
			h.applyTurnStateMissAction(ctx, target.account, settings, now)
			h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) {
				st.MissApplied, st.Status = true, "missing"
			})
		}
		return
	}
	published, putErr := store.Put(ctx, target.key, result.Ticket)
	if putErr != nil || !published {
		return
	}
	if h.turnStateMissApplied(target.stateID) {
		h.applyTurnStateRecoveredAction(ctx, target.account, settings)
	}
	h.updateTurnStateHarvestState(target.stateID, func(st *turnStateHarvestAccountState) {
		st.MissApplied, st.AuthPaused, st.Status = false, false, "fresh"
	})
}

func (h *Handler) applyTurnStateMissAction(ctx context.Context, account *auth.Account, settings database.TurnStateReuseSettings, now time.Time) {
	var err error
	switch settings.MissAction {
	case database.TurnStateMissRebindGroup:
		if settings.MissTargetGroupID != nil {
			err = h.setTurnStateAccountGroups(ctx, account, []int64{*settings.MissTargetGroupID})
		}
	case database.TurnStateMissUnbindGroups:
		err = h.setTurnStateAccountGroups(ctx, account, nil)
	case database.TurnStateMissUnschedulable:
		h.store.MarkCooldown(account, 24*time.Hour, turnStateMissReason)
	}
	if err != nil {
		log.Printf("[turn-state] apply missing action failed account=%d: %v", account.ID(), err)
	}
	_ = now
}

func (h *Handler) applyTurnStateRecoveredAction(ctx context.Context, account *auth.Account, settings database.TurnStateReuseSettings) {
	var err error
	switch settings.RecoveredAction {
	case database.TurnStateRecoveredRebind:
		if settings.RecoveredTargetGroupID != nil {
			err = h.setTurnStateAccountGroups(ctx, account, []int64{*settings.RecoveredTargetGroupID})
		}
	case database.TurnStateRecoveredRestore:
		_, err = h.store.ClearCooldownIfReason(ctx, account, turnStateMissReason)
	}
	if err != nil {
		log.Printf("[turn-state] apply recovered action failed account=%d: %v", account.ID(), err)
	}
}

func (h *Handler) setTurnStateAccountGroups(ctx context.Context, account *auth.Account, groupIDs []int64) error {
	if err := h.db.SetAccountGroups(ctx, account.ID(), groupIDs); err != nil {
		return err
	}
	h.store.ApplyAccountGroups(account.ID(), groupIDs)
	return nil
}

func (h *Handler) ensureTurnStateHarvestState(key string) turnStateHarvestAccountState {
	h.turnStateHarvestMu.Lock()
	defer h.turnStateHarvestMu.Unlock()
	state := h.turnStateHarvestStates[key]
	if state == nil {
		state = &turnStateHarvestAccountState{}
		h.turnStateHarvestStates[key] = state
	}
	return *state
}

func (h *Handler) updateTurnStateHarvestState(key string, update func(*turnStateHarvestAccountState)) {
	h.turnStateHarvestMu.Lock()
	defer h.turnStateHarvestMu.Unlock()
	state := h.turnStateHarvestStates[key]
	if state == nil {
		state = &turnStateHarvestAccountState{}
		h.turnStateHarvestStates[key] = state
	}
	update(state)
}

func (h *Handler) turnStateMissApplied(key string) bool {
	h.turnStateHarvestMu.RLock()
	defer h.turnStateHarvestMu.RUnlock()
	state := h.turnStateHarvestStates[key]
	return state != nil && state.MissApplied
}

func (h *Handler) harvestTurnStateTicket(ctx context.Context, account *auth.Account, route string, now time.Time) (turnStateHarvestResult, error) {
	first, result, err := h.harvestTurnStateCall(ctx, account, route, "", now)
	if err != nil || result.StatusCode != http.StatusOK {
		return result, err
	}
	second, result, err := h.harvestTurnStateCall(ctx, account, route, first.Raw, now)
	if err != nil || result.StatusCode != http.StatusOK {
		return result, err
	}
	result.Ticket = second
	return result, nil
}

func (h *Handler) harvestTurnStateCall(ctx context.Context, account *auth.Account, route, sentTicket string, now time.Time) (turnstate.Ticket, turnStateHarvestResult, error) {
	if account == nil {
		return turnstate.Ticket{}, turnStateHarvestResult{}, fmt.Errorf("turn-state account is nil")
	}
	endpoint := strings.TrimSpace(h.turnStateHarvestEndpoint)
	if endpoint == "" {
		endpoint = proxy.CodexBaseURL + "/responses"
	}
	body := []byte(`{"model":"gpt-6-astra","input":"Reply with OK.","stream":true,"store":false}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return turnstate.Ticket{}, turnStateHarvestResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+account.GetAccessToken())
	req.Header.Set("Chatgpt-Account-Id", account.EffectiveAccountID())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", proxy.MinimalCodexCLIUserAgentForHeaders())
	req.Header.Set("Originator", proxy.Originator)
	if strings.TrimSpace(sentTicket) != "" {
		req.Header.Set(turnstate.HeaderName, sentTicket)
	}
	clientFactory := h.turnStateHarvestClient
	if clientFactory == nil {
		clientFactory = proxy.NewUTLSHttpClient
	}
	resp, err := clientFactory(route).Do(req)
	if err != nil {
		return turnstate.Ticket{}, turnStateHarvestResult{}, err
	}
	defer resp.Body.Close()
	result := turnStateHarvestResult{StatusCode: resp.StatusCode}
	if resp.StatusCode == http.StatusTooManyRequests {
		result.RetryAfter = parseTurnStateRetryAfter(resp.Header.Get("Retry-After"), now)
		return turnstate.Ticket{}, result, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		result.AuthFailed = true
		return turnstate.Ticket{}, result, nil
	}
	if resp.StatusCode != http.StatusOK {
		return turnstate.Ticket{}, result, nil
	}
	completed, actualModel, readErr := readTurnStateHarvestSSE(resp.Body)
	if readErr != nil || !completed || actualModel != turnstate.HarvestModel {
		return turnstate.Ticket{}, result, fmt.Errorf("turn-state response not qualified")
	}
	ticket, parseErr := turnstate.Parse(resp.Header.Get(turnstate.HeaderName), now)
	if parseErr != nil {
		return turnstate.Ticket{}, result, parseErr
	}
	return ticket, result, nil
}

func readTurnStateHarvestSSE(reader io.Reader) (bool, string, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 2<<20))
	scanner.Buffer(make([]byte, 64*1024), 512*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" || !json.Valid([]byte(payload)) {
			continue
		}
		if gjson.Get(payload, "type").String() == "response.completed" {
			status := strings.TrimSpace(gjson.Get(payload, "response.status").String())
			if status != "" && status != "completed" {
				return false, "", nil
			}
			model := strings.TrimSpace(gjson.Get(payload, "response.model").String())
			if model == "" {
				model = strings.TrimSpace(gjson.Get(payload, "model").String())
			}
			return true, model, nil
		}
	}
	return false, "", scanner.Err()
}

func parseTurnStateRetryAfter(raw string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(strings.TrimSpace(raw)); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}

func redactTurnStateRoute(route string) string {
	route = strings.TrimSpace(route)
	if route == "" {
		return "direct"
	}
	parsed, err := url.Parse(route)
	if err != nil {
		return "configured_proxy"
	}
	parsed.User = nil
	return parsed.String()
}
