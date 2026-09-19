package turnstate

import (
	"net/http"
	"strings"
	"time"
)

type DecisionInput struct {
	Enabled              bool
	InScope              bool
	Endpoint             string
	Model                string
	HasCompactionTrigger bool
	Ticket               *Ticket
	Now                  time.Time
}

func ApplyOutbound(headers http.Header, in DecisionInput) bool {
	if headers == nil || !in.Enabled || !in.InScope || in.Ticket == nil || in.HasCompactionTrigger {
		return false
	}
	if strings.TrimSpace(in.Model) != HarvestModel || strings.TrimRight(strings.TrimSpace(in.Endpoint), "/") != "/responses" {
		return false
	}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	if !in.Ticket.Valid(now) {
		return false
	}
	headers.Set(HeaderName, in.Ticket.Raw)
	return true
}
