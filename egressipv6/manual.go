package egressipv6

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/codex2api/database"
)

// AssignAccount validates actual host visibility before the database atomically
// compares the current assignment and reserves the selected address.
func (m *Manager) AssignAccount(ctx context.Context, id int64, ip, expectedIP string) (database.IPv6Binding, error) {
	values := []string{ip}
	if expectedIP != "" {
		values = append(values, expectedIP)
	}
	for _, value := range values {
		addr, err := netip.ParseAddr(value)
		if err != nil || !ValidIP(addr) || addr.String() != value {
			return database.IPv6Binding{}, database.ErrIPv6InvalidSource
		}
	}
	local, err := m.LocalIPs()
	if err != nil {
		return database.IPv6Binding{}, fmt.Errorf("%w: discover local IPv6: %v", database.ErrIPv6Unavailable, err)
	}
	return m.DB.AssignIPv6Egress(ctx, id, local, ip, expectedIP, m.now().Unix())
}
