package database

import (
	"context"
	"reflect"
	"testing"
)

func TestIPv6StatusIncludesUnboundLeasedAddresses(t *testing.T) {
	db, _ := manualIPv6Fixture(t)
	ctx := context.Background()
	for _, id := range []string{"old-stream", "other-stream"} {
		if _, err := db.conn.ExecContext(ctx, `INSERT INTO ipv6_egress_leases(id,account_id,ip) VALUES($1,0,'2604::2')`, id); err != nil {
			t.Fatal(err)
		}
	}
	ips, err := db.IPv6EgressLeasedIPs(ctx)
	if err != nil || !reflect.DeepEqual(ips, []string{"2604::2"}) {
		t.Fatalf("leased addresses = %v, %v", ips, err)
	}
}
