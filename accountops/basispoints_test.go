package accountops

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBasispointsOpsQueueAndDeliveryWithoutSMTP(t *testing.T) {
	repo := &accountOpsRepoStub{}
	svc := NewAccountOpsService(&accountOpsSettingsStub{}, repo, nil)
	svc.SetModuleGate(func() bool { return true })
	svc.ObserveBasispoints(1, 4, "private error http://secret.invalid")
	if len(svc.queue) != 0 {
		t.Fatal("raw category accepted")
	}
	for i := 0; i < 300; i++ {
		svc.ObserveBasispoints(1, 4, "auto_403_disabled")
	}
	if len(svc.queue) != 256 {
		t.Fatal("queue capacity", len(svc.queue))
	}
	drop, _ := svc.RuntimeCounters()
	if drop != 44 {
		t.Fatal(drop)
	}
	event := <-svc.queue
	if event.Kind != "auto_403_disabled" || event.CredentialGeneration != 4 || event.Signal != "auto_403_disabled" {
		t.Fatal(event)
	}
	calls := 0
	svc.SetBasispointsNotifier(func(context.Context, *AccountOpsEvent) (bool, error) { calls++; return true, nil })
	svc.deliverEvent(context.Background(), &event)
	if repo.state != "sent" || calls != 1 {
		t.Fatal(repo.state, calls)
	}
	svc.SetBasispointsNotifier(func(context.Context, *AccountOpsEvent) (bool, error) { return false, errors.New("upstream rejected") })
	event.Attempts = 1
	svc.deliverEvent(context.Background(), &event)
	if repo.state != "failed" || repo.delay != 5*time.Minute {
		t.Fatal(repo.state, repo.delay)
	}
	svc.SetModuleGate(func() bool { return false })
	svc.deliverEvent(context.Background(), &event)
	if repo.state != "suppressed" {
		t.Fatal(repo.state)
	}
}
