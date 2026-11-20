package admin

import (
	"context"
	"github.com/codex2api/credentialops/siwc"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSIWCSelectionAndRevocationLeaveNativeGrantIntact(t *testing.T) {
	t.Setenv("CODEX2API_CREDENTIAL_OPS_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	h, store, db := newImportEndToEndHandler(t)
	ctx := context.Background()
	id, err := db.InsertAccountWithCredentials(ctx, "siwc-test", map[string]any{"refresh_token": "native-keep", "email": "test@example.com"}, "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetAccountByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	grant := siwc.Grant{AccessToken: "private-siwc-token", RefreshToken: "private-siwc-refresh", Subject: "subject", Email: "test@example.com", ClientID: "issued-client", Scope: "openid chatgpt.tokens.use.direct", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.SaveSIWCGrant(ctx, id, row.CredentialGeneration, 0, grant.Credentials()); err != nil {
		t.Fatal(err)
	}
	if err := store.LoadAccountByID(ctx, id); err != nil {
		t.Fatal(err)
	}
	call := func(method, body string, handler func(*gin.Context)) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Params = gin.Params{{Key: "id", Value: strconv.FormatInt(id, 10)}}
		c.Request = httptest.NewRequest(method, "/", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		if strings.Contains(rec.Body.String(), "private-siwc") || strings.Contains(rec.Body.String(), "native-keep") {
			t.Fatal("credential exposed")
		}
		return rec
	}
	if rec := call(http.MethodPut, `{"enabled":true}`, h.SelectSIWC); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !store.FindByID(id).UsesSIWC() {
		t.Fatal("selection not applied")
	}
	if rec := call(http.MethodDelete, "", h.RevokeSIWC); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !store.FindByID(id).UsesSIWC() {
		t.Fatal("revocation silently changed protocol")
	}
	if rec := call(http.MethodPut, `{"enabled":true}`, h.SelectSIWC); rec.Code != 409 {
		t.Fatal("revoked grant selected", rec.Code)
	}
	after, err := db.GetAccountByID(ctx, id)
	if err != nil || after.GetCredential("refresh_token") != "native-keep" {
		t.Fatal("native grant changed", err)
	}
}
