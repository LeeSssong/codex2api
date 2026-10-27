package wsrelay

import (
	"context"
	"fmt"
	"strings"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
)

// Keep explicit routes separate from the business pool, whose same nominal
// proxy may actually dial through Resin or inherited dialer configuration.
// The route snapshot also prevents an empty override re-reading account state.
func requestProxyKey(ctx context.Context, account *auth.Account, override string) string {
	if route, ok := proxy.AccountBoundEgress(ctx); ok {
		return "\x00account-bound|" + strings.TrimSpace(route)
	}
	if account != nil && !account.IsRelayStyle() {
		if r, enrolled, err := proxy.FixedRoute(ctx, account); enrolled && err == nil {
			return fmt.Sprintf("\x00fixed|%s|%d", r.Binding.ResourceID, r.Binding.Generation)
		}
	}
	return effectiveProxyURL(account, override)
}
