package proxy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/codex2api/auth"
	"github.com/codex2api/credentialops"
	"github.com/codex2api/database"
)

const credentialExcelClientID = auth.ExcelClientID

type credentialGrantService struct {
	db    *database.DB
	store *auth.Store
}

var credentialGrantProvider atomic.Pointer[credentialGrantService]

// ConfigureCredentialOperations connects independent Excel grants to BPS.
// The native primary credential remains the source for ordinary Codex calls.
func ConfigureCredentialOperations(db *database.DB, store *auth.Store) {
	if db == nil || store == nil {
		credentialGrantProvider.Store(nil)
		return
	}
	credentialGrantProvider.Store(&credentialGrantService{db: db, store: store})
}

func accountHasExcelOnlyGrant(account *auth.Account) bool {
	if account == nil {
		return false
	}
	account.Mu().RLock()
	id := account.OAuthClientID
	account.Mu().RUnlock()
	return id == auth.ExcelClientID
}

func (s *credentialGrantService) queueExcel(ctx context.Context, account *auth.Account) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cfg, err := s.db.GetCredentialOpsLoginConfig(ctx, account.ID())
	if err != nil || cfg == nil || cfg.CredentialMode != "password_totp" || cfg.PasswordCiphertext == "" {
		return
	}
	row, err := s.db.GetAccountByID(ctx, account.ID())
	if err != nil || !database.CredentialOperationsExcelEligible(row) {
		return
	}
	proxyURL := ""
	switch cfg.ProxySource {
	case "account":
		var usable bool
		proxyURL, usable = s.store.ResolveUsableProxyForAccount(account)
		if !usable {
			return
		}
	case "global":
		proxyURL = s.store.GetProxyURL()
	case "direct", "mihomo", "proxy": // Database freezes static/direct selectors; Mihomo is acquired at claim.
	default:
		return
	}
	cfg.OAuthProfile = "excel"
	cfg.Engine = "local_worker"
	cfg.ResolvedProxyURL = proxyURL
	cfg.RouteResolved = true
	_, _ = s.db.CreateCredentialOpsTaskWithConfig(ctx, account.ID(), row.CredentialGeneration, *cfg)
}

func credentialGrantString(grant *database.CredentialOpsProfileGrant, name string) string {
	if grant == nil {
		return ""
	}
	v, _ := grant.Credentials[name].(string)
	return strings.TrimSpace(v)
}

func credentialGrantExpires(grant *database.CredentialOpsProfileGrant) time.Time {
	raw := credentialGrantString(grant, "expires_at")
	if value, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return value
	}
	if value := auth.ParseAccessToken(credentialGrantString(grant, "access_token")); value != nil {
		return value.ExpiresAt
	}
	return time.Time{}
}

func resolveBasispointsGrant(ctx context.Context, account *auth.Account, proxyURL string) (string, int64, error) {
	s := credentialGrantProvider.Load()
	if s == nil || accountHasExcelOnlyGrant(account) {
		return account.GetAccessToken(), 0, nil
	}
	grant, err := s.db.GetCredentialOpsProfileGrant(ctx, account.ID(), "excel")
	if err != nil {
		return "", 0, routeLocalError("basispoints_excel_grant_unavailable", "Cannot read the independent Excel grant")
	}
	if grant == nil {
		cfg, e := s.db.GetCredentialOpsLoginConfig(ctx, account.ID())
		if e != nil {
			return "", 0, routeLocalError("basispoints_excel_grant_unavailable", "Cannot read login configuration")
		}
		// Accounts imported before credential operations retain their existing path.
		if cfg == nil {
			return account.GetAccessToken(), 0, nil
		}
		row, readErr := s.db.GetAccountByID(ctx, account.ID())
		if readErr != nil || !database.CredentialOperationsExcelEligible(row) {
			return "", 0, routeLocalError("basispoints_excel_account_ineligible", "This account cannot acquire Excel authorization automatically; select the native Codex path")
		}
		s.queueExcel(ctx, account)
		return "", 0, routeLocalError("basispoints_excel_auth_pending", "Excel authorization is queued in credential operations")
	}
	if grant.Invalidated || credentialGrantString(grant, "access_token") == "" {
		s.queueExcel(ctx, account)
		return "", 0, routeLocalError("basispoints_excel_auth_pending", "Excel authorization requires re-login in credential operations")
	}
	if grant.RefreshPending {
		if time.Since(grant.UpdatedAt) > 5*time.Minute {
			_, _ = s.db.InvalidateCredentialOpsProfileGrantCAS(ctx, account.ID(), "excel", grant.Generation)
			s.queueExcel(ctx, account)
		}
		return "", 0, routeLocalError("basispoints_excel_refresh_pending", "Excel refresh is in progress or needs recovery; the previous refresh token will not be reused")
	}
	expires := credentialGrantExpires(grant)
	if !expires.IsZero() && time.Until(expires) < 2*time.Minute {
		if err = s.refreshExcel(ctx, account, proxyURL, grant); err != nil {
			return "", 0, err
		}
		grant, err = s.db.GetCredentialOpsProfileGrant(ctx, account.ID(), "excel")
		if err != nil || grant == nil || grant.Invalidated {
			return "", 0, routeLocalError("basispoints_excel_auth_pending", "Excel authorization requires recovery")
		}
	}
	return credentialGrantString(grant, "access_token"), grant.Generation, nil
}

func (s *credentialGrantService) refreshExcel(ctx context.Context, account *auth.Account, proxyURL string, original *database.CredentialOpsProfileGrant) error {
	rt := credentialGrantString(original, "refresh_token")
	if rt == "" {
		s.queueExcel(ctx, account)
		return routeLocalError("basispoints_excel_auth_pending", "Excel refresh credential is missing")
	}
	return s.store.WithOAuthRefreshLease(ctx, rt, func(critical context.Context) error {
		grant, err := s.db.GetCredentialOpsProfileGrant(critical, account.ID(), "excel")
		if err != nil {
			return routeLocalError("basispoints_excel_grant_unavailable", "Cannot reload Excel authorization")
		}
		if grant == nil || grant.Invalidated {
			return routeLocalError("basispoints_excel_auth_pending", "Excel authorization requires recovery")
		}
		if grant.Generation != original.Generation || credentialGrantString(grant, "refresh_token") != rt {
			return nil
		}
		if grant.RefreshPending {
			return routeLocalError("basispoints_excel_refresh_pending", "Previous Excel refresh consumption is uncertain; re-login is required")
		}
		if expires := credentialGrantExpires(grant); !expires.IsZero() && time.Until(expires) >= 2*time.Minute {
			return nil
		}
		bound, usable := s.store.ResolveUsableProxyForAccount(account)
		if !usable {
			return routeLocalError("basispoints_excel_egress_unavailable", "Account login egress is unavailable")
		}
		if bound != "" {
			proxyURL = bound
		}
		marked, markErr := s.db.BeginCredentialOpsProfileRefresh(critical, account.ID(), "excel", grant.Generation)
		if markErr != nil || !marked {
			return routeLocalError("basispoints_excel_refresh_pending", "Cannot durably reserve the Excel refresh token")
		}
		refreshCtx := auth.WithAccountBoundEgress(critical, proxyURL)
		token, _, refreshErr := auth.RefreshAccessTokenForClient(refreshCtx, rt, proxyURL, auth.ExcelClientID, fmt.Sprint(account.ID()))
		if refreshErr != nil {
			// Do not consume an old RT again after an uncertain result or write loss.
			_, _ = s.db.InvalidateCredentialOpsProfileGrantCAS(critical, account.ID(), "excel", grant.Generation)
			s.queueExcel(critical, account)
			return routeLocalError("basispoints_excel_auth_pending", "Excel refresh failed; recovery is queued")
		}

		next := make(map[string]any, len(grant.Credentials)+1)
		for k, v := range grant.Credentials {
			next[k] = v
		}
		next["access_token"] = token.AccessToken
		next["refresh_token"] = token.RefreshToken
		next["expires_at"] = token.ExpiresAt.UTC().Format(time.RFC3339Nano)
		next["client_id"] = auth.ExcelClientID
		if token.IDToken != "" {
			next["id_token"] = token.IDToken
		}
		row, readErr := s.db.GetAccountByID(critical, account.ID())
		validationErr := readErr
		if validationErr == nil && row == nil {
			validationErr = errors.New("native account unavailable")
		}
		if validationErr == nil {
			expectedUser := row.GetCredential("user_id")
			if expectedUser == "" {
				expectedUser = row.GetCredential("chatgpt_user_id")
			}
			validationErr = credentialops.ValidateExcelRefreshCredentials(next, grant.Credentials, row.GetCredential("email"), account.EffectiveAccountID(), expectedUser)
		}
		if validationErr != nil {
			_, _ = s.db.InvalidateCredentialOpsProfileGrantCAS(critical, account.ID(), "excel", grant.Generation)
			s.queueExcel(critical, account)
			return routeLocalError("basispoints_excel_identity_mismatch", "Refreshed Excel authorization failed client and account identity validation")
		}
		ok, saveErr := s.db.UpdateCredentialOpsProfileGrantCAS(critical, account.ID(), "excel", grant.Generation, next)
		if saveErr != nil {
			_, _ = s.db.InvalidateCredentialOpsProfileGrantCAS(critical, account.ID(), "excel", grant.Generation)
			return routeLocalError("basispoints_excel_grant_unavailable", "Refreshed Excel credentials could not be saved; re-login is required")
		}
		if !ok {
			return nil
		}
		return nil
	})
}

func rejectBasispointsExcelGrant(ctx context.Context, account *auth.Account, generation int64, response *http.Response) error {
	if generation == 0 || response == nil || response.StatusCode != http.StatusUnauthorized {
		return nil
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	s := credentialGrantProvider.Load()
	if s == nil {
		return errors.New("Excel authorization provider unavailable")
	}
	if changed, err := s.db.InvalidateCredentialOpsProfileGrantCAS(ctx, account.ID(), "excel", generation); err == nil && changed {
		s.queueExcel(ctx, account)
	}
	return routeLocalError("basispoints_excel_auth_pending", "Excel authorization was rejected; recover it in credential operations")
}
