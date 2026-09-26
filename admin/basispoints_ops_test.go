package admin

import (
	"context"
	"database/sql"
	"github.com/codex2api/accountops"
	"github.com/codex2api/database"
	"github.com/codex2api/tokenguard"
	"strings"
	"testing"
)

type bpsBarkCapture struct {
	calls       int
	title, body string
}

func (c *bpsBarkCapture) SendNotification(_ context.Context, _ tokenguard.Config, title, body string, _ bool) error {
	c.calls++
	c.title = title
	c.body = body
	return nil
}
func TestBasispointsOpsBarkUsesOptInAndRejectsStale(t *testing.T) {
	h, ids, _ := newPagedAccountsHandler(t)
	ctx := context.Background()
	if e := database.NewAccountOpsSettings(h.db).Set(ctx, "module_enabled", "true"); e != nil {
		t.Fatal(e)
	}
	row, e := h.db.GetAccountByID(ctx, ids[0])
	if e != nil {
		t.Fatal(e)
	}
	repo := database.NewAccountOpsRepository(h.db)
	event := accountops.AccountOpsEvent{AccountID: ids[0], CredentialGeneration: row.CredentialGeneration, Kind: "auto_403_disabled", Signal: "private-http://sensitive"}
	if e = repo.Record(ctx, event); e != nil {
		t.Fatal(e)
	}
	claimed, e := repo.Claim(ctx)
	if e != nil || claimed == nil {
		t.Fatal(claimed, e)
	}
	client := &bpsBarkCapture{}
	notify := h.basispointsOpsNotifier(client)
	sent, e := notify(ctx, claimed)
	if e != nil || sent || client.calls != 0 {
		t.Fatal("unconfigured Bark sent", sent, e)
	}
	_, revision, e := h.db.TokenGuardConfig(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.db.SaveTokenGuardConfig(ctx, "{\"bark_key\":\"synthetic\",\"notify_on_fail\":true}", revision); e != nil {
		t.Fatal(e)
	}
	sent, e = notify(ctx, claimed)
	if e != nil || !sent || client.calls != 1 || strings.Contains(client.body, "private") || strings.Contains(client.body, "http") {
		t.Fatal(sent, client.calls, e)
	}
	e = h.db.WithAccountControlTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "UPDATE accounts SET credential_generation=credential_generation+1 WHERE id=$1", ids[0])
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	sent, e = notify(ctx, claimed)
	if e != nil || sent || client.calls != 1 {
		t.Fatal("stale notification sent", sent, client.calls, e)
	}
}
