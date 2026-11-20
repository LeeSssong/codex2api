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
