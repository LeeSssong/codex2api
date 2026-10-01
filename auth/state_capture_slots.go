package auth

// SetStateBusinessLimit is a transient admission budget. Zero restores the
// normal scheduler; existing requests are never interrupted by a limit change.
func (a *Account) SetStateBusinessLimit(limit int) {
	a.stateAdmissionMu.Lock()
	a.stateBusinessLimit = int64(max(0, limit))
	a.stateAdmissionMu.Unlock()
}

func (s *Store) TakeStateCaptureAccount(id int64, filter AccountFilter) *Account {
	account, _ := s.takeByIDModeWithCapacity(id, 0, nil, filter, false, "", DispatchPolicyStandard, true)
	return account
}

func releaseStateCaptureSlot(account *Account) bool {
	account.stateAdmissionMu.Lock()
	defer account.stateAdmissionMu.Unlock()
	account.stateCaptureRequests = max(0, account.stateCaptureRequests-1)
	return releaseOccupiedAccountSlot(account)
}

func (s *Store) ReleaseStateCapture(account *Account) {
	if account != nil && releaseStateCaptureSlot(account) {
		s.notifySchedulerAccountAvailability(account, false)
	}
}
