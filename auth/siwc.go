package auth

func (a *Account) UsesSIWC() bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.SIWCEnabled
}

func (a *Account) SetSIWCEnabled(enabled bool) {
	a.mu.Lock()
	a.SIWCEnabled = enabled
	a.mu.Unlock()
}

// ApplySIWCSelection updates the scheduler projection after a durable selection.
func (s *Store) ApplySIWCSelection(id int64, enabled bool) {
	if account := s.FindByID(id); account != nil {
		account.SetSIWCEnabled(enabled)
		s.fastSchedulerUpdate(account)
	}
}
