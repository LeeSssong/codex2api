package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Deferred telemetry selects the live global Resin target when sent. A fixture
// used as that target must not consume a Responses attempt for analytics.
func TestAttemptSequenceFixtureIgnoresQueuedTelemetry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		make   func(*testing.T, [][]string) (*httptest.Server, *atomic.Int32)
		events [][]string
	}{
		{"events", newAttemptSequenceSSEServer, [][]string{{`{"type":"first-attempt"}`}, {`{"type":"second-attempt"}`}}},
		{"raw", newAttemptSequenceRawSSEServer, [][]string{{"data: first-attempt\n\n"}, {"data: second-attempt\n\n"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := GetResinConfig()
			t.Cleanup(func() { SetResinConfig(previous) })
			manager := newCodexTelemetryManager()
			manager.queue <- codexTelemetryJob{client: testCodexTelemetryProfile().client, url: "https://chatgpt.com/backend-api/codex/analytics-events/events", body: []byte(`{"events":[]}`)}
			server, calls := tc.make(t, tc.events)
			SetResinConfig(&ResinConfig{BaseURL: server.URL, PlatformName: "test"})
			if err := sendCodexTelemetryJob(<-manager.queue); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatalf("telemetry consumed Responses attempt: %d", calls.Load())
			}
			for i, want := range []string{"first-attempt", "second-attempt"} {
				response, err := http.Post(BuildReverseProxyURL("https://chatgpt.com/backend-api/codex/responses"), "application/json", strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(body), want) || calls.Load() != int32(i+1) {
					t.Fatalf("attempt=%d body=%s calls=%d", i, body, calls.Load())
				}
			}
		})
	}
}
