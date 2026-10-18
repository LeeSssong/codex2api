package database

import (
	"context"
	"testing"
	"time"
)

func TestPostMigrationStartupDoesNotReuseExpiredConnectionDeadline(t *testing.T) {
	parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	ctx, stop := postMigrationStartupContext(parent)
	defer stop()
	if ctx.Err() != nil {
		t.Fatal("successful data migration inherited the expired connection-test budget")
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) < 9*time.Second || time.Until(deadline) > 11*time.Second {
		t.Fatal("small schema initialization does not have its independent bounded budget")
	}
}
