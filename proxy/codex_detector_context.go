package proxy

import (
	"context"
	"errors"
	"github.com/codex2api/auth"
	"time"
)

// codexDetectorRequestKey marks an administrator-triggered model fingerprint
// probe. Detector requests keep their measurement payload isolated from user
// payload rules and telemetry, while their outbound protocol identity (device
// fingerprint and timezone) follows ordinary Codex requests.
type codexDetectorRequestKey struct{}

// WithCodexDetectorRequest marks ctx as a detector probe context.
func WithCodexDetectorRequest(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexDetectorRequestKey{}, true)
}

func isCodexDetectorRequest(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	marked, _ := ctx.Value(codexDetectorRequestKey{}).(bool)
	return marked
}

// ErrAutomaticIntelligenceCheckDeferred is a scheduling outcome, not a quality
// observation. Only automatic probes opt in; manual and business requests do not.
var ErrAutomaticIntelligenceCheckDeferred = errors.New("automatic intelligence check deferred: account unavailable")

type automaticIntelligenceCheckKey struct{}

func WithAutomaticIntelligenceCheck(ctx context.Context) context.Context {
	return context.WithValue(ctx, automaticIntelligenceCheckKey{}, true)
}

// CheckAutomaticIntelligenceCheckDispatch rechecks live quota after potentially
// blocking request preparation/pool acquisition, immediately before transport.
func CheckAutomaticIntelligenceCheckDispatch(ctx context.Context, account *auth.Account) error {
	automatic, _ := ctx.Value(automaticIntelligenceCheckKey{}).(bool)
	if automatic && !account.AutomaticIntelligenceCheckAvailable(time.Now()) {
		return ErrAutomaticIntelligenceCheckDeferred
	}
	return nil
}
