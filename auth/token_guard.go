package auth

import (
	"context"
	"errors"
	"github.com/codex2api/database"
	"log"
	"sort"
	"time"
)

// External password/MFA authentication has already completed before this method
// is called. Native leases protect only the bounded reread/CAS/publication phase.
func (s *Store) PublishTokenGuardCredentials(ctx context.Context, j database.TokenGuardJob, snapshot database.TokenGuardAccount, updates map[string]any) (*database.TokenGuardAccount, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("原生凭据存储不可用")
	}
	// No RT is consumed here. Honor caller cancellation and bound the entire
	// acquisition/CAS phase well below native four-minute critical leases.
	publishCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	oldRT := snapshot.Credential("refresh_token")
	newRT, _ := updates["refresh_token"].(string)
	tokens := []string{}
	if oldRT != "" {
		tokens = append(tokens, oldRT)
	}
	if newRT != "" && newRT != oldRT {
		tokens = append(tokens, newRT)
	}
	if len(tokens) == 0 {
		return nil, errors.New("重登返回的凭据不完整")
	}
	sort.Slice(tokens, func(i, j int) bool {
		return oauthRefreshTokenFingerprint(tokens[i]) < oauthRefreshTokenFingerprint(tokens[j])
	})
	for _, token := range tokens {
		lease, err := s.acquireOAuthRefreshLease(publishCtx, token)
		if err != nil {
			return nil, err
		}
		defer lease.Release()
	}
	fresh, err := s.db.PublishTokenGuardCredentials(publishCtx, j, snapshot, updates)
	if err != nil {
		return nil, err
	}
	// Only a successful database COMMIT proves persistence. Projection failure
	// afterwards must not cause another external login; the outbox retries it.
	if err = s.SyncTokenGuardAccount(publishCtx, snapshot.ID); err != nil {
		log.Printf("[token-guard] account=%d credentials committed; native projection pending", snapshot.ID)
	}
	return fresh, nil
}
func (s *Store) SyncTokenGuardAccount(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return errors.New("原生凭据存储不可用")
	}
	var cacheErr error
	if s.tokenCache != nil {
		cacheErr = s.tokenCache.DeleteAccessToken(ctx, id)
	}
	if err := s.reloadDispatchAccountByID(ctx, id); err != nil {
		return err
	}
	if account := s.FindByID(id); account != nil {
		if err := account.ReloadCodexRoutes(ctx); err != nil {
			return err
		}
	}
	return cacheErr
}
