package fixedegress

import (
	"context"
	"github.com/codex2api/database"
	"sync/atomic"
	"time"
)

type Route struct {
	Binding  database.FixedEgressBinding
	Pool     database.FixedEgressPool
	Enrolled bool
}

var current atomic.Pointer[Manager]

func Current() *Manager  { return current.Load() }
func Install(m *Manager) { current.Store(m) }
func Enabled() bool {
	m := Current()
	if m == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, e := m.db.GetFixedEgressConfig(ctx)
	return e != nil || c.Enabled
}
func HasEnrolled(id int64) bool {
	m := Current()
	if m == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	yes, e := m.HasEnrollment(ctx, id)
	return e != nil || yes
}
func (m *Manager) DB() *database.DB { return m.db }
func (m *Manager) HasEnrollment(ctx context.Context, id int64) (bool, error) {
	return m.db.HasFixedEgressEnrollment(ctx, id)
}
func (m *Manager) Acquire(ctx context.Context, id int64) (Route, error) {
	enrolled, e := m.HasEnrollment(ctx, id)
	r := Route{Enrolled: enrolled}
	if e != nil {
		r.Enrolled = true
		return r, e
	}
	if !enrolled {
		return r, nil
	}
	r.Binding, e = m.db.EnsureFixedEgressBinding(ctx, id)
	if e != nil {
		return r, e
	}
	r.Pool, e = m.db.GetFixedEgressPool(ctx, r.Binding.PoolID)
	return r, e
}
func (m *Manager) Failure(ctx context.Context, id, generation int64, resource, reason string) error {
	return m.db.MarkFixedEgressFailure(ctx, id, generation, resource, reason)
}

// InUse includes disabled enrollments: they must be explicitly removed before
// another egress mode can take over, otherwise credentials could change route.
func (m *Manager) InUse(ctx context.Context) (bool, error) {
	c, e := m.db.GetFixedEgressConfig(ctx)
	if e != nil {
		return false, e
	}
	n, e := m.db.CountFixedEgressBindings(ctx)
	return c.Enabled || n > 0, e
}
