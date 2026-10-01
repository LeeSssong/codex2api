package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestConnectionTurnStateLengthArrivesBeforeContent(t *testing.T) {
	for _, size := range []int{0, 292, 312} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			value := strings.Repeat("s", size)
			handler, _, _ := newCodexDiagnosticsTestHandler(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if value != "" {
					w.Header().Set("X-Codex-Turn-State", value)
				}
				_, _ = io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"overloaded\"}}}\n\n")
			})
			body := serveCodexDiagnosticsTest(handler).Body.String()
			events := decodeCodexTestEvents(t, body)
			initial := events[1].CodexDiagnostics
			if initial == nil || initial.TurnStateLength == nil || *initial.TurnStateLength != size || initial.TurnStateSource != "http_headers" || initial.DurationMS != nil {
				t.Fatalf("initial diagnostics must contain exact upstream length: %+v", initial)
			}
			if events[len(events)-2].Type != "error" {
				t.Fatal("a matched length must not turn an upstream failure into test success")
			}
			final := events[len(events)-1].CodexDiagnostics
			if final.TurnStateLength == nil || *final.TurnStateLength != size {
				t.Fatal("final diagnostics lost the state length")
			}
			if value != "" && strings.Contains(body, value) {
				t.Fatal("diagnostics must not expose the raw state")
			}
		})
	}
}

func TestCodexTurnStateDistinguishesHandshakeFromResponse(t *testing.T) {
	value := strings.Repeat("a", 292)
	next := strings.Repeat("b", 312)
	headers := make(http.Header)
	headers.Set("Upgrade", "websocket")
	headers.Set("X-Codex-Turn-State", value)
	metadata := `{"type":"response.metadata","headers":{"x-codex-turn-state":"` + next + `"}}`
	resp := &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(strings.NewReader(metadata))}
	r := newCodexTestRecorder(resp, "gpt-6-astra", nil, time.Now())
	if r.details.TurnStateLength == nil || *r.details.TurnStateLength != 292 || r.details.TurnStateSource != "ws_handshake" {
		t.Fatal("handshake state must be clearly identified")
	}
	if r.capture.Len() != 0 {
		t.Fatal("header observation must not consume the response body")
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	r.observe(data)
	r.observe([]byte(`{"type":"response.metadata","headers":{"x-request-id":"req-test"}}`))
	if r.details.TurnStateLength == nil || *r.details.TurnStateLength != 312 || r.details.TurnStateSource != "response_metadata" {
		t.Fatal("per-response state must replace handshake state and survive unrelated metadata")
	}
	raw, err := json.Marshal(r.finish())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), value) || strings.Contains(string(raw), next) || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatal("state must be redacted in both metadata headers and the captured body")
	}
}

func TestCodexTurnStateTransportFailureLeavesLengthUnknown(t *testing.T) {
	r := newCodexTestRecorder(nil, "gpt-6-astra", nil, time.Now())
	if r.finish().TurnStateLength != nil || r.details.TurnStateSource != "" {
		t.Fatal("no upstream response must not be reported as an absent header")
	}
}
