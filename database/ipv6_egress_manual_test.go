package database

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func manualIPv6Fixture(t *testing.T) (*DB, []string) {
	t.Helper()
	db := newModelQualityDB(t)
	ctx := context.Background()
	for _, id := range []int64{1, 2} {
		if err := db.UpdateCredentials(ctx, id, map[string]interface{}{"ipv6_egress_enabled": true}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := db.IPv6EgressConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	cfg.CooldownSeconds = 600
	ips := []string{"2604::1", "2604::2", "2604::3"}
	cfg.SourceIPs = ips[:2]
	if err = db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	return db, ips
}

func TestIPv6ManualSelectionRejectsIneligibleAndUnavailable(t *testing.T) {
	for _, name := range []string{"not enrolled", "proxy", "pool off", "not local", "outside allowlist", "other binding", "any lease", "cooldown", "retired", "stale expected"} {
		t.Run(name, func(t *testing.T) {
			db, ips := manualIPv6Fixture(t)
			ctx := context.Background()
			target, expected := ips[0], ""
			want := ErrIPv6SelectionConflict
			var err error
			switch name {
			case "not enrolled":
				err = db.UpdateCredentials(ctx, 1, map[string]interface{}{"ipv6_egress_enabled": false})
				want = ErrIPv6NotEnrolled
			case "proxy":
				_, err = db.conn.Exec(`UPDATE accounts SET proxy_url='http://proxy.invalid:8080' WHERE id=1`)
				want = ErrIPv6NotEnrolled
			case "pool off":
				_, err = db.conn.Exec(`UPDATE ipv6_egress_config SET enabled=FALSE`)
				want = ErrIPv6Unavailable
			case "not local":
				target = "2604::9"
				want = ErrIPv6InvalidSource
			case "outside allowlist":
				target = ips[2]
				want = ErrIPv6InvalidSource
			case "other binding":
				_, _, err = db.ClaimIPv6Egress(ctx, 2, ips, "", "", 1000, true)
			case "any lease":
				_, err = db.conn.Exec(`INSERT INTO ipv6_egress_leases(id,account_id,ip) VALUES('bootstrap',0,$1)`, target)
			case "cooldown":
				_, err = db.conn.Exec(`INSERT INTO ipv6_egress_cooldowns(ip,until_time) VALUES($1,2000)`, target)
			case "retired":
				_, _, err = db.ClaimIPv6Egress(ctx, 1, ips, "", "", 1000, true)
				if err == nil {
					err = db.TrackIPv6EgressLease(ctx, "retiring", 1, target)
				}
				if err == nil {
					err = db.RetireIPv6Egress(ctx, 1, 1001)
				}
				expected = target
				target = ips[1]
			case "stale expected":
				expected = ips[1]
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.AssignIPv6Egress(ctx, 1, ips, target, expected, 1002); !errors.Is(err, want) {
				t.Fatalf("got %v want %v", err, want)
			}
			got, err := db.CurrentIPv6EgressIP(ctx, 1)
			if err != nil || got != "" {
				t.Fatalf("rejection created/activated binding: %q %v", got, err)
			}
		})
	}
}

func TestIPv6ManualSelectionOldLeasesDrainAndFenceFailures(t *testing.T) {
	db, ips := manualIPv6Fixture(t)
	ctx := context.Background()
	first, err := db.AssignIPv6Egress(ctx, 1, ips, ips[0], "", 1000)
	if err != nil || first.Reason != "manual" || first.Rotations != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	for _, lease := range []string{"http", "ws"} {
		if err = db.TrackIPv6EgressLease(ctx, lease, 1, first.IP); err != nil {
			t.Fatal(err)
		}
	}
	same, err := db.AssignIPv6Egress(ctx, 1, ips, first.IP, first.IP, 1001)
	if err != nil || same != first {
		t.Fatalf("idempotent lease-bearing current IP: %+v %v", same, err)
	}
	next, err := db.AssignIPv6Egress(ctx, 1, ips, ips[1], first.IP, 1010)
	if err != nil || next.IP != ips[1] || next.Rotations != 2 || next.Reason != "manual" {
		t.Fatalf("switch: %+v %v", next, err)
	}
	var until int64
	if err = db.conn.QueryRow(`SELECT until_time FROM ipv6_egress_cooldowns WHERE ip=$1`, first.IP).Scan(&until); err != nil || until != 1610 {
		t.Fatalf("initial cooldown: %d %v", until, err)
	}
	delayed, _, err := db.ClaimIPv6Egress(ctx, 1, ips, first.IP, "http_429", 1020, true)
	if err != nil || delayed != next {
		t.Fatalf("delayed failure changed manual assignment: %+v %v", delayed, err)
	}
	if err = db.TrackIPv6EgressLease(ctx, "stale", 1, first.IP); !errors.Is(err, ErrIPv6Unavailable) {
		t.Fatalf("stale route lease: %v", err)
	}
	// Cooldown has elapsed, but old connections on either instance still reserve it.
	if _, err = db.AssignIPv6Egress(ctx, 2, ips, first.IP, "", 1700); !errors.Is(err, ErrIPv6SelectionConflict) {
		t.Fatalf("old leases reused: %v", err)
	}
	if err = db.ReleaseIPv6EgressLease(ctx, "http", 1700); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AssignIPv6Egress(ctx, 2, ips, first.IP, "", 1701); !errors.Is(err, ErrIPv6SelectionConflict) {
		t.Fatalf("WS lease reused: %v", err)
	}
	if err = db.ReleaseIPv6EgressLease(ctx, "ws", 1800); err != nil {
		t.Fatal(err)
	}
	if err = db.conn.QueryRow(`SELECT until_time FROM ipv6_egress_cooldowns WHERE ip=$1`, first.IP).Scan(&until); err != nil || until != 2400 {
		t.Fatalf("cooldown not restarted at final drain: %d %v", until, err)
	}
	if _, err = db.AssignIPv6Egress(ctx, 2, ips, first.IP, "", 2399); !errors.Is(err, ErrIPv6SelectionConflict) {
		t.Fatalf("drain cooldown reused: %v", err)
	}
	if _, err = db.AssignIPv6Egress(ctx, 2, ips, first.IP, "", 2400); err != nil {
		t.Fatal(err)
	}
}

func TestIPv6ManualSelectionConcurrentExpectedIP(t *testing.T) {
	db, ips := manualIPv6Fixture(t)
	ctx := context.Background()
	cfg, err := db.IPv6EgressConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.SourceIPs = ips
	if err = db.SaveIPv6EgressConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	first, err := db.AssignIPv6Egress(ctx, 1, ips, ips[0], "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	// Independent database handles model separate running instances sharing one DB.
	var seq int
	var name, path string
	if err = db.conn.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	other, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, ip := range ips[1:] {
		writer := db
		if i == 1 {
			writer = other
		}
		wg.Add(1)
		go func(writer *DB, ip string) {
			defer wg.Done()
			_, err := writer.AssignIPv6Egress(ctx, 1, ips, ip, first.IP, 1010)
			results <- err
		}(writer, ip)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrIPv6SelectionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestIPv6ManualSelectionPreservesLongerCooldown(t *testing.T) {
	db, ips := manualIPv6Fixture(t)
	ctx := context.Background()
	first, err := db.AssignIPv6Egress(ctx, 1, ips, ips[0], "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.TrackIPv6EgressLease(ctx, "long", 1, first.IP); err != nil {
		t.Fatal(err)
	}
	if _, err = db.conn.Exec(`INSERT INTO ipv6_egress_cooldowns(ip,until_time) VALUES($1,9000)`, first.IP); err != nil {
		t.Fatal(err)
	}
	if _, err = db.AssignIPv6Egress(ctx, 1, ips, ips[1], first.IP, 1010); err != nil {
		t.Fatal(err)
	}
	if err = db.ReleaseIPv6EgressLease(ctx, "long", 1020); err != nil {
		t.Fatal(err)
	}
	var until int64
	if err = db.conn.QueryRow(`SELECT until_time FROM ipv6_egress_cooldowns WHERE ip=$1`, first.IP).Scan(&until); err != nil || until != 9000 {
		t.Fatalf("shortened longer cooldown: %d %v", until, err)
	}
}
