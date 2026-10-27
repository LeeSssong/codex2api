package auth

// LockCredentialOpsRecovery orders a native login commit and runtime reload
// against in-flight cooldown publishers. Callers must release before waiting
// for any background usage probes. The gate never covers upstream login I/O.
func (s *Store) LockCredentialOpsRecovery() func() {
	if s == nil {
		return func() {}
	}
	s.credentialRecoveryMu.Lock()
	return s.credentialRecoveryMu.Unlock
}
