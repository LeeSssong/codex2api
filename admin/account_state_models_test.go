package admin

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"github.com/codex2api/ipv6state"
	"github.com/codex2api/statepool"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccountStateFilterPrecedesPaginationAndBulkSelection(t *testing.T) {
	h, ids, _ := newPagedAccountsHandler(t)
	account := h.store.FindByID(ids[2])
	account.Mu().Lock()
	account.AccessToken = "h." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"state-member"}`)) + ".sig"
	account.AccountID = "state-workspace"
	account.Mu().Unlock()
	h.ipv6State = ipv6state.New(h.db, h.store, nil, nil, nil)
	if err := h.ipv6State.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.ipv6State.Stop)
	identity, err := statepool.Snapshot(account, "")
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 217)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(time.Now().Unix()-60))
	value := base64.URLEncoding.EncodeToString(raw)
	if err := h.ipv6State.Import(context.Background(), ipv6state.Portable{Format: "codex2api-ipv6-292-v1", MemberHash: identity.MemberHash, WorkspaceHash: identity.WorkspaceHash, Model: statepool.Models[0], Value: value}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		total int
	}{
		{"state=valid", 1}, {"state=valid&state_model=gpt-5.6-sol", 1}, {"state=valid&state_model=gpt-5.6-terra", 0}, {"state=missing&state_model=gpt-5.6-sol", 2},
	} {
		r := invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&page=1&page_size=1&"+tc.query)
		if r.Code != 200 {
			t.Fatalf("query failed: %d", r.Code)
		}
		var page accountsPageResponse
		if err := json.Unmarshal(r.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != tc.total {
			t.Fatalf("%s total %d != %d", tc.query, page.Total, tc.total)
		}
		if strings.HasPrefix(tc.query, "state=valid") && tc.total > 0 && page.Accounts[0].ID != ids[2] {
			t.Fatal("filter applied after pagination")
		}
		if strings.Contains(r.Body.String(), value) {
			t.Fatal("account page leaked State")
		}
	}
	selected, err := h.resolveAccountOperationSelector(context.Background(), &accountOperationSelector{Channel: "codex", State: "valid", StateModel: statepool.Models[0]})
	if err != nil || len(selected) != 1 || selected[0] != ids[2] {
		t.Fatal("bulk selection ignored State filter")
	}
	r := invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&state=valid&state_model=unknown")
	if r.Code != 400 {
		t.Fatal("unsupported model was accepted")
	}
	config := h.ipv6State.Status().Config
	config.Models = []string{"gpt-5.6-sol", "gpt-5.6-luna"}
	if err := h.ipv6State.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	r = invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&page_size=1&state=valid&state_model=gpt-5.6-terra")
	var selectedPage accountsPageResponse
	if err := json.Unmarshal(r.Body.Bytes(), &selectedPage); err != nil {
		t.Fatal(err)
	}
	if selectedPage.Total != 1 || len(selectedPage.Accounts[0].StateModels) != 2 || selectedPage.StateSummary.ReuseAccounts != 1 || selectedPage.StateSummary.ValidCombinations != 1 {
		t.Fatal("deselected model did not fall back before pagination")
	}
	selected, err = h.resolveAccountOperationSelector(context.Background(), &accountOperationSelector{Channel: "codex", State: "valid", StateModel: "gpt-5.6-terra"})
	if err != nil || len(selected) != 1 || selected[0] != selectedPage.Accounts[0].ID {
		t.Fatal("bulk and paginated fallback differed")
	}
	account.SetCooldownUntil(time.Now().Add(time.Hour), "rate_limited")
	r = invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&state=available")
	if json.Unmarshal(r.Body.Bytes(), &selectedPage) != nil || selectedPage.Total != 0 || selectedPage.StateSummary.ReuseAccounts != 1 || selectedPage.StateSummary.AvailableAccounts != 0 {
		t.Fatal("available filter included a cooling account or lost saved coverage")
	}
	selected, err = h.resolveAccountOperationSelector(context.Background(), &accountOperationSelector{Channel: "codex", State: "available"})
	if err != nil || len(selected) != 0 {
		t.Fatal("bulk selection ignored current State availability")
	}
	account.Mu().Lock()
	account.CredentialGeneration++
	account.Mu().Unlock()
	r = invokeListAccounts(t, h, "/api/admin/accounts?view=page&channel=codex&state=valid")
	var page accountsPageResponse
	if json.Unmarshal(r.Body.Bytes(), &page) != nil || page.Total != 0 {
		t.Fatal("cached list reused old credential State")
	}
}

func TestStatePolicyPatchPreservesCaptureSettings(t *testing.T) {
	h, _, _ := newPagedAccountsHandler(t)
	h.ipv6State = ipv6state.New(h.db, h.store, nil, nil, nil)
	if err := h.ipv6State.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.ipv6State.Stop)
	router := gin.New()
	h.registerIPv6StateRoutes(router.Group("/state-pool"))
	req := httptest.NewRequest(http.MethodPatch, "/state-pool/ipv6/policy", strings.NewReader(`{"require_valid_state":true}`))
	req.Header.Set("Content-Type", "application/json")
	r := httptest.NewRecorder()
	router.ServeHTTP(r, req)
	config := h.ipv6State.Status().Config
	if r.Code != 200 || !config.RequireValidState || config.Enabled || config.Concurrency != 20 {
		t.Fatal("policy patch changed capture settings")
	}
	config.RequireValidState = false
	if err := h.ipv6State.Configure(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if !h.ipv6State.Status().Config.RequireValidState {
		t.Fatal("stale capture settings reset policy")
	}
}
