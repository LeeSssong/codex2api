package egressipv6

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"github.com/codex2api/database"
	"sync"
	"time"
)

// Selection is fetched durably so OAuth (which only has an account ID) and stale
// transports follow the same membership as freshly loaded runtime accounts.
func (m *Manager) Selection(ctx context.Context, id int64) (configured, enabled bool, proxyURL string, err error) {
	row, err := m.DB.GetAccountByID(ctx, id)
	if err != nil {
		return false, false, "", err
	}
	return row.GetCredentialOptionalBool("ipv6_egress_enabled") != nil, row.GetCredentialBool("ipv6_egress_enabled") && row.ProxyURL == "", row.ProxyURL, nil
}
func (m *Manager) AcquireAccount(ctx context.Context, id int64) (Route, func(), error) {
	_, enabled, _, err := m.Selection(ctx, id)
	if err != nil {
		return Route{}, nil, err
	}
	if !enabled {
		return Route{}, nil, database.ErrIPv6NotEnrolled
	}
	route, err := m.claim(ctx, id, "", "", true)
	if err != nil {
		return route, nil, err
	}
	if !route.Config.Enabled || route.Binding.IP == "" {
		return route, nil, database.ErrIPv6Unavailable
	}
	release, err := m.TrackAccountRoute(ctx, id, route.Binding.IP)
	return route, release, err
}
func (m *Manager) TrackAccountRoute(ctx context.Context, id int64, ip string) (func(), error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	leaseID := hex.EncodeToString(b[:])
	if err := m.DB.TrackIPv6EgressLease(ctx, leaseID, id, ip); err != nil {
		return nil, err
	}
	return m.leaseRelease(leaseID), nil
}

func (m *Manager) AcquireBootstrap(ctx context.Context) (string, func(), error) {
	local, err := m.LocalIPs()
	if err != nil {
		return "", nil, err
	}
	local, err = ValidateSources(local, local)
	if err != nil {
		return "", nil, err
	}
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	leaseID := hex.EncodeToString(b[:])
	ip, err := m.DB.ReserveIPv6BootstrapLease(ctx, leaseID, local, m.now().Unix())
	if err != nil {
		return "", nil, err
	}
	return ip, m.leaseRelease(leaseID), nil
}
func (m *Manager) leaseRelease(leaseID string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := m.DB.ReleaseIPv6EgressLease(c, leaseID, m.now().Unix()); err != nil {
				fmt.Printf("[IPv6Egress] lease release failed: %v\n", err)
			}
		})
	}
}

func (m *Manager) RetireAccount(ctx context.Context, id int64) error {
	return m.DB.RetireIPv6Egress(ctx, id, m.now().Unix())
}

func (m *Manager) RotateAccount(ctx context.Context, id int64, ip, reason string) (Route, error) {
	_, enabled, _, err := m.Selection(ctx, id)
	if err != nil {
		return Route{}, err
	}
	if !enabled {
		return Route{}, database.ErrIPv6NotEnrolled
	}
	return m.claim(ctx, id, ip, reason, true)
}
