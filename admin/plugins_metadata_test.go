package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codex2api/database"
	"github.com/codex2api/plugins"
	"github.com/gin-gonic/gin"
)

func TestPluginHTTPMutationCannotReplaceInstalledMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "plugins.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := database.NewPluginStore(db)
	if err := store.Put(context.Background(), plugins.Setting{ID: "quality-ops", Version: "old-version", SourceSHA: "old-source", SDKCompatibility: "old-sdk", UpdateMode: "old-mode", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db, pluginRegistry: plugins.NewRegistry(store)}
	installed, err := h.pluginRegistry.Get(context.Background(), "quality-ops")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Version == "old-version" || installed.SourceSHA == "old-source" || installed.Enabled {
		t.Fatal("stored control metadata hid the installed build")
	}
	router := gin.New()
	router.PUT("/plugins/:id", h.UpdatePlugin)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/plugins/quality-ops", strings.NewReader(`{"enabled":false,"flags":{"alerts":true},"version":"forged","source_sha":"forged","sdk_compatibility":"forged","update_mode":"forged"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mutation status %d: %s", w.Code, w.Body.String())
	}
	var got plugins.Setting
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != installed.Version || got.SourceSHA != installed.SourceSHA || got.SDKCompatibility != installed.SDKCompatibility || got.UpdateMode != installed.UpdateMode || got.Enabled || !got.Flags["alerts"] {
		t.Fatalf("mutation replaced installed metadata or lost controls: %+v", got)
	}
	stored, err := store.Get(context.Background(), "quality-ops")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != installed.Version || stored.SourceSHA != installed.SourceSHA {
		t.Fatal("forged or old installed metadata persisted after mutation")
	}
}
