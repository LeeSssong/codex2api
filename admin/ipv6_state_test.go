package admin

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/codex2api/database"
	"github.com/codex2api/ipv6state"
)

func TestIPv6CaptureSkipsDisabledGateways(t *testing.T) {
	ctx := context.Background()
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	disabledID, err := db.InsertProxy(ctx, "http://disabled.invalid:8080", "disabled")
	if err != nil {
		t.Fatal(err)
	}
	enabledID, err := db.InsertProxy(ctx, "http://enabled.invalid:8080", "enabled")
	if err != nil {
		t.Fatal(err)
	}
	enabled := false
	if err := db.UpdateProxy(ctx, disabledID, nil, nil, &enabled); err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db}
	config := ipv6state.DefaultConfig()
	config.ProxyIDs = []int64{disabledID, enabledID}
	for attempt := range int64(4) {
		route, err := h.ipv6StateRoute(ctx, config, attempt)
		if err != nil || route.ProxyID != enabledID {
			t.Fatalf("disabled gateway blocked the pool: %v", err)
		}
	}
	config.ForwardProxyID = disabledID
	if _, err := h.ipv6StateRoute(ctx, config, 0); err == nil {
		t.Fatal("disabled forward proxy was silently bypassed")
	}
}
