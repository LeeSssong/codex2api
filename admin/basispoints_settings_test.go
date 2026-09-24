package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
)

func TestBasispointsSettingsPersistReloadAndPartialUpdates(t *testing.T) {
	previous := proxy.CurrentRuntimeSettings()
	t.Cleanup(func() { proxy.ApplyRuntimeSettings(previous) })
	handler, db, _ := newResponseCacheSettingsAdminHandler(t)
	initial := invokeResponseCacheSettingsAdmin(t, handler, http.MethodGet, nil)
	if initial.Code != http.StatusOK || decodeResponseCacheSettingsResponse(t, initial).CodexBasispointsEnabled {
		t.Fatal("Basispoints must default to disabled")
	}
	for _, enabled := range []bool{true, false} {
		response := invokeResponseCacheSettingsAdmin(t, handler, http.MethodPut, map[string]any{"codex_basispoints_enabled": enabled})
		if response.Code != http.StatusOK || decodeResponseCacheSettingsResponse(t, response).CodexBasispointsEnabled != enabled {
			t.Fatalf("setting update failed: %d %s", response.Code, response.Body)
		}
		if proxy.CurrentRuntimeSettings().CodexBasispointsEnabled != enabled {
			t.Fatal("setting did not apply immediately")
		}
		unrelated := invokeResponseCacheSettingsAdmin(t, handler, http.MethodPut, map[string]any{"site_name": "Basispoints test"})
		if unrelated.Code != http.StatusOK || decodeResponseCacheSettingsResponse(t, unrelated).CodexBasispointsEnabled != enabled {
			t.Fatal("unrelated settings update changed upstream selection")
		}
		saved, err := db.GetSystemSettings(context.Background())
		if err != nil || saved.CodexBasispointsEnabled != enabled {
			t.Fatalf("setting was not persisted: %+v %v", saved, err)
		}
		restarted := auth.NewStore(nil, nil, saved)
		if restarted.CodexBasispointsEnabled() != enabled || proxy.ApplyRuntimeSettingsFromSystem(saved).CodexBasispointsEnabled != enabled {
			t.Fatal("setting was lost on reload")
		}
		restarted.Stop()
	}
}
