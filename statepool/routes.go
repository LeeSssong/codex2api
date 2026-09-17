package statepool

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/security"
)

func proxyRef(row *database.ProxyRow) ProxyRef {
	name := row.Label
	if name == "" {
		name = fmt.Sprintf("Proxy #%d", row.ID)
	}
	ip := ""
	if row.TestStatus == "ok" || row.TestStatus == "success" {
		if parsed, err := netip.ParseAddr(row.TestIP); err == nil {
			ip = parsed.Unmap().String()
		}
	}
	dynamic := SupportsSessionRotation(row.URL)
	if dynamic {
		ip = ""
	}
	return ProxyRef{ID: row.ID, Name: name, LastTestIP: ip, URLHash: Hash(row.URL), Dynamic: dynamic}
}

func (m *Manager) captureRoutes(ctx context.Context, options CaptureOptions) ([]ProxyRef, error) {
	if len(options.ProxyIDs) > 12 {
		return nil, errors.New("select at most 12 capture proxies")
	}
	seen := map[string]bool{}
	routes := []ProxyRef{}
	for _, id := range options.ProxyIDs {
		row, err := m.db.GetProxy(ctx, id)
		if err != nil || row == nil || !row.Enabled {
			return nil, fmt.Errorf("proxy %d is unavailable", id)
		}
		if _, err := security.ParseProxyURL(row.URL); err != nil {
			return nil, fmt.Errorf("proxy %d has an invalid URL", id)
		}
		ref := proxyRef(row)
		ref.RotateSession = options.NewSession && SupportsSessionRotation(row.URL)
		if ref.RotateSession {
			ref.LastTestIP = ""
		}
		key := ref.URLHash
		if options.DistinctIPs && ref.LastTestIP != "" {
			key = ref.LastTestIP
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		routes = append(routes, ref)
	}
	return routes, nil
}

func (m *Manager) resolveCaptureProxy(ctx context.Context, ref ProxyRef, business string) (string, ProxyRef, error) {
	if ref.ID == 0 {
		resolved, err := m.businessProxyRef(ctx, business)
		return business, resolved, err
	}
	row, err := m.db.GetProxy(ctx, ref.ID)
	if err != nil || row == nil || !row.Enabled || Hash(row.URL) != ref.URLHash {
		return "", ref, errors.New("capture_proxy_changed_or_unavailable")
	}
	if ref.RotateSession {
		proxyURL, err := proxyWithSession(row.URL, ref.SessionID)
		ref.LastTestIP = ""
		return proxyURL, ref, err
	}
	return row.URL, proxyRef(row), nil
}

func (m *Manager) businessProxyRef(ctx context.Context, proxyURL string) (ProxyRef, error) {
	rows, err := m.db.ListProxies(ctx)
	if err != nil {
		return ProxyRef{}, err
	}
	for _, row := range rows {
		if row.URL == proxyURL {
			return proxyRef(row), nil
		}
	}
	return ProxyRef{Name: "Business egress", URLHash: Hash(proxyURL)}, nil
}

type forwardProxyKey struct{}

// CaptureForwardProxy is request-scoped and never contains OAuth credentials.
func CaptureForwardProxy(ctx context.Context) string {
	value, _ := ctx.Value(forwardProxyKey{}).(string)
	return value
}

func (m *Manager) captureForwardRoute(ctx context.Context, options CaptureOptions) (ProxyRef, error) {
	if options.ForwardProxyID == 0 {
		return ProxyRef{}, nil
	}
	if len(options.ProxyIDs) == 0 {
		return ProxyRef{}, errors.New("a forward proxy requires a selected capture proxy")
	}
	for _, id := range options.ProxyIDs {
		if id == options.ForwardProxyID {
			return ProxyRef{}, errors.New("forward proxy cannot also be a capture proxy")
		}
	}
	refs, err := m.captureRoutes(ctx, CaptureOptions{ProxyIDs: []int64{options.ForwardProxyID}})
	if err != nil {
		return ProxyRef{}, err
	}
	return refs[0], nil
}

func (m *Manager) reserveRoute(ctx context.Context, job Job, route string, forward ...string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := m.db.AcquireStatePoolRoute(ctx, job.ID, m.owner, route, forward...)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) cancelStopped(ctx context.Context) {
	rows, err := m.db.ListStatePoolJobs(ctx)
	if err != nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, row := range rows {
		if row.Status == "cancelling" {
			if cancel := m.cancels[row.ID]; cancel != nil {
				cancel()
			}
		}
	}
}

func (m *Manager) CancelGroup(ctx context.Context, group string) error {
	if err := m.db.CancelStatePoolGroup(ctx, group); err != nil {
		return err
	}
	m.cancelStopped(ctx)
	return nil
}
