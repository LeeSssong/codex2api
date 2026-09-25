package proxy

import (
	"bytes"
	"context"
	"github.com/codex2api/accountops"
	"github.com/codex2api/auth"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestAccountOpsObserverDoesNotConsumeOrRewriteResponse(t *testing.T) {
	body := []byte(`{"error":{"code":"insufficient_balance"}}`)
	r := &http.Response{StatusCode: 402, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body))}
	observeAccountOpsResponse(&auth.Account{DBID: 1}, r)
	got, e := io.ReadAll(r.Body)
	if e != nil || !bytes.Equal(got, body) {
		t.Fatal("response changed")
	}
	r.Body.Close()
}

type accountOpsObserverSettings struct{}

func (accountOpsObserverSettings) GetValue(context.Context, string) (string, error) {
	return `{"enabled":true,"recipient":"ops@example.test","balance_low":true,"weekly_quota":true,"cooldown_minutes":60}`, nil
}
func (accountOpsObserverSettings) Set(context.Context, string, string) error { return nil }

type accountOpsObserverRepo struct {
	events chan accountops.AccountOpsEvent
}

func (r *accountOpsObserverRepo) Record(_ context.Context, e accountops.AccountOpsEvent) error {
	r.events <- e
	return nil
}
func (*accountOpsObserverRepo) Claim(context.Context) (*accountops.AccountOpsEvent, error) {
	return nil, nil
}
func (*accountOpsObserverRepo) Complete(context.Context, *accountops.AccountOpsEvent, string, time.Duration) error {
	return nil
}
func (*accountOpsObserverRepo) SuppressDisabled(context.Context, accountops.AccountOpsConfig) error {
	return nil
}
func (*accountOpsObserverRepo) List(context.Context, int, int) ([]accountops.AccountOpsEvent, error) {
	return nil, nil
}
func TestAccountOpsEnabledObserverQueuesSanitizedSignalWithoutChangingBody(t *testing.T) {
	repo := &accountOpsObserverRepo{events: make(chan accountops.AccountOpsEvent, 1)}
	svc := accountops.NewAccountOpsService(accountOpsObserverSettings{}, repo, nil)
	if _, e := svc.GetConfig(context.Background()); e != nil {
		t.Fatal(e)
	}
	svc.Start()
	defer svc.Stop()
	SetAccountOpsObserver(svc)
	defer SetAccountOpsObserver(nil)
	body := []byte(`{"error":{"code":"insufficient_balance","message":"secret credential"}}`)
	r := &http.Response{StatusCode: 402, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
	observeAccountOpsResponse(&auth.Account{DBID: 44, Email: "test"}, r)
	observeAccountOpsResponse(&auth.Account{DBID: 44, Email: "test"}, r)
	got, e := io.ReadAll(r.Body)
	r.Body.Close()
	if e != nil || !bytes.Equal(got, body) {
		t.Fatal("response modified")
	}
	select {
	case event := <-repo.events:
		if event.AccountID != 44 || event.Kind != "balance_low" || event.Signal != "balance_error_code" {
			t.Fatalf("wrong event %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event not queued")
	}
	select {
	case <-repo.events:
		t.Fatal("nested executor duplicated event")
	default:
	}
}
