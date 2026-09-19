package turnstate

import (
	"net/http"
	"testing"
	"time"
)

func TestApplyOutboundOnlyOverwritesInScopeAstraResponses(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ticket, err := Parse(encodedTicket(t, 217, now.Add(-time.Minute)), now)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		in   DecisionInput
		want bool
	}{
		{"astra", DecisionInput{Enabled: true, InScope: true, Endpoint: "/responses", Model: HarvestModel, Ticket: &ticket, Now: now}, true},
		{"sol", DecisionInput{Enabled: true, InScope: true, Endpoint: "/responses", Model: "gpt-5.6-sol", Ticket: &ticket, Now: now}, false},
		{"compact endpoint", DecisionInput{Enabled: true, InScope: true, Endpoint: "/responses/compact", Model: HarvestModel, Ticket: &ticket, Now: now}, false},
		{"compact body", DecisionInput{Enabled: true, InScope: true, Endpoint: "/responses", Model: HarvestModel, HasCompactionTrigger: true, Ticket: &ticket, Now: now}, false},
		{"out of scope", DecisionInput{Enabled: true, Endpoint: "/responses", Model: HarvestModel, Ticket: &ticket, Now: now}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{"X-Codex-Turn-State": []string{"client-state"}}
			applied := ApplyOutbound(h, tc.in)
			if applied != tc.want {
				t.Fatalf("applied = %v, want %v", applied, tc.want)
			}
			if tc.want && h.Get(HeaderName) != ticket.Raw {
				t.Fatalf("header = %q", h.Get(HeaderName))
			}
			if !tc.want && h.Get(HeaderName) != "client-state" {
				t.Fatalf("no-op changed header to %q", h.Get(HeaderName))
			}
		})
	}
}
