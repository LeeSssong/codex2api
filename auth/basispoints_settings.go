package auth

// SetCodexBasispointsEnabled changes the global inference route for Codex accounts.
func (s *Store) SetCodexBasispointsEnabled(enabled bool) {
	if s != nil {
		s.codexBasispointsEnabled.Store(enabled)
	}
}

// CodexBasispointsEnabled reports the current pool-wide upstream selection.
func (s *Store) CodexBasispointsEnabled() bool {
	return s != nil && s.codexBasispointsEnabled.Load()
}
