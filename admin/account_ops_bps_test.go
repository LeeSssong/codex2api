package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/database"
	"github.com/codex2api/proxy"
)

func TestAccountOpsNormalQualityJobRecordsActualBPSPath(t *testing.T) {
	for _, path := range []string{"basispoints", "codex"} {
		t.Run(path, func(t *testing.T) {
			previous, resin := proxy.CurrentRuntimeSettings(), proxy.GetResinConfig()
			defer func() { proxy.ApplyRuntimeSettings(previous); proxy.SetResinConfig(resin) }()
			settings := proxy.DefaultRuntimeSettings()
			settings.CodexBasispointsEnabled = path == "basispoints"
			settings.CodexForceWebsocket = false
			settings.CodexRequestCompression = false
			proxy.ApplyRuntimeSettings(settings)
			t.Setenv("CODEX_TRANSPORT_MODE", "standard")
			ctx := context.Background()
			db := newTestAdminDB(t)
			store := auth.NewStore(db, nil, nil)
			defer store.Stop()
			reached := ""
			wireModel := ""
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = r.URL.Path
				body, _ := io.ReadAll(r.Body)
				var request map[string]any
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
				}
				wireModel, _ = request["model"].(string)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"21\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"model\":\"gpt-6-astra\"}}\n\n")
			}))
			defer upstream.Close()
			proxy.SetResinConfig(&proxy.ResinConfig{BaseURL: upstream.URL, PlatformName: "quality-path-test"})
			id, err := db.InsertAccountWithCredentials(ctx, "quality-path", map[string]any{"access_token": "test-token", "account_id": "workspace"}, "")
			if err != nil {
				t.Fatal(err)
			}
			row, err := db.GetAccountByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			account := &auth.Account{DBID: id, AccountID: "workspace", AccessToken: "test-token", Models: []string{"gpt-6-astra"}, Status: auth.StatusReady, CredentialGeneration: row.CredentialGeneration}
			store.AddAccount(account)
			job, err := db.CreateQualityTestJob(ctx, database.QualityTestJob{AccountID: id, AccountName: "quality-path", Model: "gpt-6-astra", Prompt: "q"})
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{db: db, store: store}
			h.runQualityTestJob(ctx, *job, qualityTestRequest{Model: "gpt-6-astra", Prompt: "q"})
			got, err := db.GetQualityTestJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(got)
			var record map[string]any
			json.Unmarshal(raw, &record)
			host := "chatgpt.com"
			if path == "basispoints" {
				host = "bps.openai.com"
			}
			if !strings.Contains(reached, "/"+host+"/") || wireModel != "gpt-6-astra" {
				t.Fatalf("wrong actual transport path=%s model=%s", reached, wireModel)
			}
			if got.Status != "completed" || got.Output != "21" {
				t.Fatalf("quality result %+v", got)
			}
			if record["upstream"] != path || record["credential_generation"] != float64(row.CredentialGeneration) {
				t.Fatalf("actual path/generation missing in saved quality job: %s", raw)
			}
		})
	}
}
