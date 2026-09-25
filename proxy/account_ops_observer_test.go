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

func TestAccountOpsObservesActualCodexRoutesOnce(t *testing.T) {
	for _, path := range []string{"native", "basispoints", "native_fallback"} {
		t.Run(path, func(t *testing.T) {
			enableBasispointsForTest(t)
			settings := CurrentRuntimeSettings()
			settings.CodexForceWebsocket = false
			settings.CodexRequestCompression = false
			if path == "native" {
				settings.CodexBasispointsEnabled = false
			}
			ApplyRuntimeSettings(settings)
			repo := &accountOpsObserverRepo{events: make(chan accountops.AccountOpsEvent, 8)}
			svc := accountops.NewAccountOpsService(accountOpsObserverSettings{}, repo, nil)
			if _, err := svc.GetConfig(context.Background()); err != nil {
				t.Fatal(err)
			}
			svc.Start()
			defer svc.Stop()
			SetAccountOpsObserver(svc)
			defer SetAccountOpsObserver(nil)
			account := &auth.Account{DBID: 94120, AccountID: "workspace", AccessToken: "test-token"}
			body := []byte("{\"model\":\"gpt-6-astra\",\"input\":\"hello\"}")
			if path == "native_fallback" {
				body = []byte("{\"model\":\"gpt-6-astra\",\"input\":\"hello\",\"tools\":[{\"type\":\"web_search\",\"external_web_access\":true}]}")
			}
			reached := ""
			transport := func(req *http.Request) (*http.Response, error) {
				reached = req.URL.Host
				return &http.Response{StatusCode: 402, Header: make(http.Header), Body: io.NopCloser(bytes.NewBufferString("{\"error\":{\"code\":\"insufficient_balance\"}}"))}, nil
			}
			installBasispointsTransport(t, account, transport)
			installClaudeBoundaryTransport(t, account, transport)
			resp, err := ExecuteRequest(context.Background(), account, body, "", "", "", nil, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			_, err = io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if path == "basispoints" && reached != "bps.openai.com" {
				t.Fatalf("BPS did not run: %s", reached)
			}
			if path != "basispoints" && reached != "chatgpt.com" {
				t.Fatalf("native did not run: %s", reached)
			}
			select {
			case e := <-repo.events:
				if e.AccountID != account.DBID || e.Kind != "balance_low" {
					t.Fatalf("wrong event %+v", e)
				}
			case <-time.After(time.Second):
				t.Fatal("actual upstream failure not observed")
			}
			select {
			case <-repo.events:
				t.Fatal("upstream failure observed twice")
			default:
			}
		})
	}
}

func TestAccountOpsObservesBPSCompactTerminalFailure(t *testing.T) {
	enableBasispointsForTest(t)
	repo := &accountOpsObserverRepo{events: make(chan accountops.AccountOpsEvent, 2)}
	svc := accountops.NewAccountOpsService(accountOpsObserverSettings{}, repo, nil)
	if _, err := svc.GetConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc.Start()
	defer svc.Stop()
	SetAccountOpsObserver(svc)
	defer SetAccountOpsObserver(nil)
	account := &auth.Account{DBID: 95121, AccountID: "workspace", AccessToken: "test-token"}
	installBasispointsTransport(t, account, func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(bytes.NewBufferString("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"insufficient_balance\",\"message\":\"billing failed\"}}}\n\n"))}, nil
	})
	resp, err := executeBasispointsCompactRequest(context.Background(), account, []byte("{\"model\":\"gpt-6-astra\",\"input\":\"summarize\"}"), "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 502 {
		t.Fatalf("terminal failure status %d", resp.StatusCode)
	}
	select {
	case e := <-repo.events:
		if e.Kind != "balance_low" {
			t.Fatalf("wrong event %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("BPS compact terminal failure not observed")
	}
	select {
	case <-repo.events:
		t.Fatal("terminal failure observed twice")
	default:
	}
}
