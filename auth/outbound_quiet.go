package auth

import "sync/atomic"

// AdministrativelyDisabled reports whether an operator set enabled=false.
// That flag is DispatchPaused. Disabled is a separate 401 fence and must not
// be treated as an administrative disable.
func (a *Account) AdministrativelyDisabled() bool {
	return a != nil && atomic.LoadInt32(&a.DispatchPaused) != 0
}
