package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/codex2api/database"
)

func TestCodexBPSModelPolicyKeyCreateUpdateAndReload(t *testing.T) {
	db := newTestAdminDB(t)
	h := &Handler{db: db}
	limits := database.APIKeyLimits{CodexRoutePolicy: database.CodexRouteBasispointsModelsOnly, ModelAllow: []string{"gpt-*"}}
	rec := callModelRequestKeyMutation(t, h, http.MethodPost, 0, map[string]any{
		"name": "Model routing", "key": "sk-synthetic-model-policy-1234567890", "limits": limits,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created createAPIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []map[string]any{{"name": "Renamed"}, {"limits": limits}} {
		rec = callModelRequestKeyMutation(t, h, http.MethodPatch, created.ID, edit)
		if rec.Code != http.StatusOK {
			t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
		}
		row, err := db.GetAPIKeyByID(context.Background(), created.ID)
		if err != nil || row.Limits.CodexRoutePolicy != limits.CodexRoutePolicy || len(row.Limits.ModelAllow) != 1 {
			t.Fatalf("policy or model permissions lost: %v", err)
		}
	}
	limits.UpstreamChannel = database.UpstreamChannelGrok
	rec = callModelRequestKeyMutation(t, h, http.MethodPatch, created.ID, map[string]any{"limits": limits})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("incompatible channel accepted: %d", rec.Code)
	}
}
