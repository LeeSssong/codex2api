package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRefreshAccessTokenPreservesOAuthProfileClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("client_id") != ExcelClientID {
			t.Errorf("wrong OAuth client: %s", r.Form.Get("client_id"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"excel-new","refresh_token":"excel-new-rt","expires_in":3600}`))
	}))
	defer server.Close()
	old := ResinRequestDecorator
	t.Cleanup(func() { ResinRequestDecorator = old })
	ResinRequestDecorator = func(string, string) string { return server.URL }
	token, _, err := RefreshAccessTokenForClient(context.Background(), "excel-old-rt", "", ExcelClientID, "37")
	if err != nil || token.AccessToken != "excel-new" {
		t.Fatalf("refresh: %v %v", token, err)
	}
}
