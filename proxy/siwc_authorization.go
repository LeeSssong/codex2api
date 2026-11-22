package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/credentialops/siwc"
	"github.com/codex2api/database"
	"github.com/google/uuid"
)

type siwcAuthorization struct {
	worker                                 *database.SIWCWorkerFence
	accountID, generation, grantGeneration int64
	route, hostID                          string
	expected                               siwc.Identity
	workspace                              string
	invalidated                            bool
	session                                *siwc.BrowserSession
	client                                 *http.Client
	release                                func()
	timer                                  *time.Timer
}

var siwcAuthorizations = struct {
	sync.Mutex
	sessions map[string]*siwcAuthorization
}{sessions: make(map[string]*siwcAuthorization)}

type SIWCAuthorizationResult struct {
	ProxyURL  string `json:"-"`
	SessionID string `json:"session_id"`
	AuthURL   string `json:"auth_url"`
}

func siwcRouteRevision(row *database.AccountRow) string {
	raw, _ := json.Marshal([]any{row.ProxyURL, row.GetCredentialOptionalBool(auth.IPv6EgressCredentialKey), row.GetCredential("email"), row.GetCredential("workspace_id")})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func CancelSIWCAuthorization(id string) { closeSIWCSession(id) }

func closeSIWCSession(id string) {
	siwcAuthorizations.Lock()
	session := siwcAuthorizations.sessions[id]
	delete(siwcAuthorizations.sessions, id)
	siwcAuthorizations.Unlock()
	if session != nil {
		session.timer.Stop()
		session.release()
	}
}

func BeginSIWCAuthorization(ctx context.Context, accountID int64) (SIWCAuthorizationResult, error) {
	return beginSIWCAuthorization(ctx, accountID, nil)
}

func BeginSIWCWorkerAuthorization(ctx context.Context, accountID int64, worker database.SIWCWorkerFence) (SIWCAuthorizationResult, error) {
	return beginSIWCAuthorization(ctx, accountID, &worker)
}

func beginSIWCAuthorization(ctx context.Context, accountID int64, worker *database.SIWCWorkerFence) (SIWCAuthorizationResult, error) {
	service := credentialGrantProvider.Load()
	if service == nil {
		return SIWCAuthorizationResult{}, errors.New("credential runtime unavailable")
	}
	row, err := service.db.GetAccountByID(ctx, accountID)
	if err != nil || row == nil || !database.CredentialOperationsEligible(row) || row.GetCredential("email") == "" {
		return SIWCAuthorizationResult{}, errors.New("an OpenAI account with an expected login email is required")
	}
	if err := service.store.LoadAccountByID(ctx, accountID); err != nil {
		return SIWCAuthorizationResult{}, err
	}
	account := service.store.FindByID(accountID)
	if account == nil {
		return SIWCAuthorizationResult{}, errors.New("account runtime unavailable")
	}
	tunnel, release, err := StartAccountEgressTunnel(account)
	if err != nil {
		return SIWCAuthorizationResult{}, err
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()
	previous, err := service.db.GetCredentialOpsProfileGrant(ctx, accountID, "siwc")
	if err != nil {
		return SIWCAuthorizationResult{}, err
	}
	clientID, hostID := "dynamic_agent_client", siwc.NewHostID()
	expected := siwc.Identity{Email: row.GetCredential("email")}
	var generation int64
	invalidated := false
	if previous != nil {
		grant, err := siwc.DecodeGrant(previous.Credentials)
		if err != nil {
			return SIWCAuthorizationResult{}, err
		}
		clientID, hostID, expected.Subject = grant.ClientID, grant.HostID, grant.Subject
		generation, invalidated = previous.Generation, previous.Invalidated
	}
	route := siwcRouteRevision(row)
	session, location, err := siwc.BeginBrowser(clientID, hostID, fmt.Sprint(accountID), route, time.Now())
	if err != nil {
		return SIWCAuthorizationResult{}, err
	}
	parsed, _ := url.Parse(location)
	query := parsed.Query()
	query.Set("login_hint", expected.Email)
	parsed.RawQuery = query.Encode()
	location = parsed.String()
	id := uuid.NewString()
	entry := &siwcAuthorization{accountID: accountID, generation: row.CredentialGeneration, grantGeneration: generation, route: route, hostID: hostID, expected: expected, workspace: row.GetCredential("workspace_id"), invalidated: invalidated, session: session, client: auth.BuildUTLSHTTPClient(tunnel), release: release}
	siwcAuthorizations.Lock()
	if len(siwcAuthorizations.sessions) >= 100 {
		siwcAuthorizations.Unlock()
		return SIWCAuthorizationResult{}, errors.New("too many pending SIWC authorizations")
	}
	siwcAuthorizations.sessions[id] = entry
	entry.worker = worker
	entry.timer = time.AfterFunc(30*time.Minute, func() { closeSIWCSession(id) })
	siwcAuthorizations.Unlock()
	ok = true
	return SIWCAuthorizationResult{SessionID: id, AuthURL: location, ProxyURL: tunnel}, nil
}

func CompleteSIWCAuthorization(ctx context.Context, accountID int64, sessionID, callback string) error {
	return completeSIWCAuthorization(ctx, accountID, sessionID, callback, nil)
}

func CompleteSIWCWorkerAuthorization(ctx context.Context, accountID int64, sessionID, callback string, worker database.SIWCWorkerFence) error {
	return completeSIWCAuthorization(ctx, accountID, sessionID, callback, &worker)
}

func completeSIWCAuthorization(ctx context.Context, accountID int64, sessionID, callback string, worker *database.SIWCWorkerFence) error {
	service := credentialGrantProvider.Load()
	if service == nil {
		return errors.New("credential runtime unavailable")
	}
	siwcAuthorizations.Lock()
	entry := siwcAuthorizations.sessions[sessionID]
	if entry == nil || entry.accountID != accountID {
		siwcAuthorizations.Unlock()
		return errors.New("SIWC session unavailable")
	}
	if (entry.worker == nil) != (worker == nil) || (worker != nil && *entry.worker != *worker) {
		siwcAuthorizations.Unlock()
		return errors.New("SIWC task ownership changed")
	}
	delete(siwcAuthorizations.sessions, sessionID)
	entry.timer.Stop()
	siwcAuthorizations.Unlock()
	defer entry.release()
	row, err := service.db.GetAccountByID(ctx, accountID)
	if err != nil || row == nil || row.CredentialGeneration != entry.generation || siwcRouteRevision(row) != entry.route {
		return errors.New("account changed; restart SIWC authorization")
	}
	input, err := entry.session.TakeCallback(callback, fmt.Sprint(accountID), entry.route, time.Now())
	if err != nil {
		return err
	}
	grant, err := siwc.Exchange(ctx, entry.client, input, entry.hostID, entry.expected)
	if err != nil {
		return err
	}
	grant.Workspace = entry.workspace
	if worker != nil {
		return service.db.CommitSIWCWorkerGrant(ctx, accountID, entry.generation, entry.grantGeneration, grant.Credentials(), entry.invalidated, *worker)
	}
	if entry.invalidated {
		return service.db.RebindInvalidatedSIWCGrant(ctx, accountID, entry.generation, entry.grantGeneration, grant.Credentials())
	}
	return service.db.SaveSIWCGrant(ctx, accountID, entry.generation, entry.grantGeneration, grant.Credentials())
}

func resolveSIWCGrant(ctx context.Context, account *auth.Account, client *http.Client) (siwc.Grant, int64, error) {
	service := credentialGrantProvider.Load()
	if service == nil {
		return siwc.Grant{}, 0, errors.New("credential runtime unavailable")
	}
	stored, err := service.db.GetCredentialOpsProfileGrant(ctx, account.ID(), "siwc")
	if err != nil || stored == nil || stored.Invalidated {
		return siwc.Grant{}, 0, errors.New("SIWC authorization required")
	}
	grant, err := siwc.DecodeGrant(stored.Credentials)
	if err != nil {
		return siwc.Grant{}, 0, err
	}
	row, err := service.db.GetAccountByID(ctx, account.ID())
	if err != nil || row == nil || row.GetCredential("email") != grant.Email || row.GetCredential("workspace_id") != grant.Workspace {
		return siwc.Grant{}, 0, errors.New("SIWC account binding changed")
	}
	if time.Until(grant.ExpiresAt) > 2*time.Minute && !stored.RefreshPending {
		return grant, stored.Generation, nil
	}
	err = service.store.WithOAuthRefreshLease(ctx, grant.RefreshToken, func(critical context.Context) error {
		fresh, err := service.db.GetCredentialOpsProfileGrant(critical, account.ID(), "siwc")
		if err != nil || fresh == nil || fresh.Invalidated {
			return errors.New("SIWC authorization required")
		}
		if fresh.RefreshPending {
			return errors.New("SIWC refresh outcome uncertain; reauthorize")
		}
		current, err := siwc.DecodeGrant(fresh.Credentials)
		if err != nil {
			return err
		}
		if time.Until(current.ExpiresAt) > 2*time.Minute {
			return nil
		}
		row, err := service.db.GetAccountByID(critical, account.ID())
		if err != nil {
			return err
		}
		if row == nil || row.GetCredential("email") != current.Email || row.GetCredential("workspace_id") != current.Workspace {
			return errors.New("SIWC account binding changed")
		}
		marked, err := service.db.BeginCredentialOpsProfileRefresh(critical, account.ID(), "siwc", fresh.Generation)
		if err != nil || !marked {
			return errors.New("SIWC refresh already reserved")
		}
		next, err := siwc.Refresh(critical, client, current)
		persist, cancel := context.WithTimeout(context.WithoutCancel(critical), 5*time.Second)
		defer cancel()
		if err != nil {
			_, _ = service.db.InvalidateCredentialOpsProfileGrantCAS(persist, account.ID(), "siwc", fresh.Generation)
			return err
		}
		if err = service.db.SaveSIWCRefreshedGrant(persist, row, fresh.Generation, next.Credentials()); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return siwc.Grant{}, 0, err
	}
	stored, err = service.db.GetCredentialOpsProfileGrant(ctx, account.ID(), "siwc")
	if err != nil || stored == nil || stored.Invalidated || stored.RefreshPending {
		return siwc.Grant{}, 0, errors.New("SIWC refreshed credentials unavailable")
	}
	grant, err = siwc.DecodeGrant(stored.Credentials)
	return grant, stored.Generation, err
}
