package auth

const IPv6EgressCredentialKey = "ipv6_egress_enabled"

// Missing membership preserves the legacy group/global policy. An explicit
// false with a blank proxy means host IPv4 and must not inherit another route.
func (a *Account) IPv6EgressSelection() (configured, enabled bool) {
	if a == nil {
		return false, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.isRelayStyleLocked() {
		return false, false
	}
	return a.IPv6EgressConfigured, a.IPv6EgressEnabled && a.ProxyURL == ""
}
func (a *Account) HostIPv4Egress() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return !a.isRelayStyleLocked() && a.ProxyURL == "" && !a.IPv6EgressEnabled
}
func (s *Store) ApplyAccountIPv6Selection(id int64, enabled bool) {
	if a := s.FindByID(id); a != nil {
		a.mu.Lock()
		a.IPv6EgressConfigured = true
		a.IPv6EgressEnabled = enabled
		a.mu.Unlock()
	}
}
