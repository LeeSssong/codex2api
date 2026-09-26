package database

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImageAssetGalleryExcludesInbound(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, model := range []string{"gpt-image-1", "bps-inbound"} {
		if _, err := db.InsertImageAsset(ctx, ImageAssetInput{Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := db.ListImageAssets(ctx, 1, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 1 || len(p.Assets) != 1 || p.Assets[0].Model != "gpt-image-1" {
		t.Fatalf("private inbound image leaked to gallery: %+v", p)
	}
}

func TestImageAssetRelayAtomicReservationsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	first, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := New("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	type result struct {
		token string
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, db := range []*DB{first, second} {
		go func(db *DB) {
			<-start
			token, err := db.ReserveImageRelay(ctx, 700<<20, 300)
			results <- result{token, err}
		}(db)
	}
	close(start)
	a, b := <-results, <-results
	if (a.err == nil) == (b.err == nil) {
		t.Fatalf("want exactly one admitted, got %v / %v", a.err, b.err)
	}
	u, err := first.GetImageRelayUsage(ctx)
	if err != nil || u.ReservedBytes != 700<<20 || u.ReservedAssets != 300 {
		t.Fatalf("quota overbooked: %+v %v", u, err)
	}
	if a.err != nil && !errors.Is(a.err, ErrImageRelayCapacity) {
		t.Fatalf("unexpected rejection: %v", a.err)
	}
	if b.err != nil && !errors.Is(b.err, ErrImageRelayCapacity) {
		t.Fatalf("unexpected rejection: %v", b.err)
	}
	token := a.token
	if token == "" {
		token = b.token
	}
	if err := second.FinishImageRelay(ctx, token, false, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ReserveImageRelay(ctx, 1<<30, 512); err != nil {
		t.Fatalf("released capacity not available: %v", err)
	}
}
func TestImageAssetRelayPendingCleanupRetainsQuota(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "assets.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	token, err := db.ReserveImageRelay(ctx, 100, 1)
	if err != nil {
		t.Fatal(err)
	}
	id, reused, err := db.PrepareImageRelay(ctx, token, "scope", "digest", 1, ImageAssetInput{Bytes: 100, StoragePath: "/pending/image.png"})
	if err != nil || reused {
		t.Fatalf("prepare %v %v", reused, err)
	}
	if err := db.FinishImageRelay(ctx, token, false, nil, 1); err != nil {
		t.Fatal(err)
	}
	u, err := db.GetImageRelayUsage(ctx)
	if err != nil || u.Bytes != 100 || u.Assets != 1 || u.CleanupPending != 1 || u.ReservedBytes != 0 {
		t.Fatalf("cleanup capacity disappeared: %+v %v", u, err)
	}
	if _, _, err := db.PrepareImageRelay(ctx, token, "scope", "digest", 1, ImageAssetInput{Bytes: 100}); err == nil {
		t.Fatal("deleting object reused")
	}
	if err := db.DeleteCleanedImageRelay(ctx, id); err != nil {
		t.Fatal(err)
	}
	u, _ = db.GetImageRelayUsage(ctx)
	if u.Bytes != 0 || u.Assets != 0 {
		t.Fatalf("cleaned object still charged: %+v", u)
	}
}

func TestImageAssetRelayPostgresAtomicAndCleanup(t *testing.T) {
	dsn := os.Getenv("CODEX2API_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL fixture")
	}
	schema := "image_relay_" + relayToken()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	firstConn, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer firstConn.Close()
	ctx := context.Background()
	if _, err = firstConn.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer firstConn.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	_, err = firstConn.ExecContext(ctx, "CREATE TABLE image_assets(id BIGSERIAL PRIMARY KEY,job_id BIGINT DEFAULT 0,template_id BIGINT DEFAULT 0,filename TEXT DEFAULT '',storage_path TEXT DEFAULT '',mime_type TEXT DEFAULT '',bytes BIGINT DEFAULT 0,width INTEGER DEFAULT 0,height INTEGER DEFAULT 0,model TEXT DEFAULT '',requested_size TEXT DEFAULT '',actual_size TEXT DEFAULT '',quality TEXT DEFAULT '',output_format TEXT DEFAULT '',revised_prompt TEXT DEFAULT '',created_at TIMESTAMPTZ DEFAULT NOW())")
	if err != nil {
		t.Fatal(err)
	}
	first := &DB{conn: firstConn, driver: "postgres"}
	if err = first.ensureImageRelaySchema(ctx); err != nil {
		t.Fatal(err)
	}
	secondConn, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer secondConn.Close()
	second := &DB{conn: secondConn, driver: "postgres"}
	t.Run("CleanupRetryFairness", func(t *testing.T) { testImageRelayCleanupRetryFairness(t, second) })
	type result struct {
		token string
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, db := range []*DB{first, second} {
		go func(db *DB) {
			<-start
			token, err := db.ReserveImageRelay(ctx, 700<<20, 300)
			results <- result{token, err}
		}(db)
	}
	close(start)
	a, b := <-results, <-results
	if (a.err == nil) == (b.err == nil) {
		t.Fatalf("atomic admission failed: %v / %v", a.err, b.err)
	}
	if a.err != nil && !errors.Is(a.err, ErrImageRelayCapacity) {
		t.Fatalf("unexpected rejection: %v", a.err)
	}
	if b.err != nil && !errors.Is(b.err, ErrImageRelayCapacity) {
		t.Fatalf("unexpected rejection: %v", b.err)
	}
	token := a.token
	if token == "" {
		token = b.token
	}
	id, _, err := second.PrepareImageRelay(ctx, token, "scope", "digest", 3, ImageAssetInput{Bytes: 100, StoragePath: "/test/pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err = first.FinishImageRelay(ctx, token, false, nil, 3); err != nil {
		t.Fatal(err)
	}
	usage, err := second.GetImageRelayUsage(ctx)
	if err != nil || usage.Bytes != 100 || usage.ReservedBytes != 0 || usage.CleanupPending != 1 {
		t.Fatalf("pending cleanup accounting failed: %+v %v", usage, err)
	}
	assets, err := first.ClaimExpiredImageRelay(ctx, time.Now(), 10)
	if err != nil || len(assets) != 1 || assets[0].ID != id {
		t.Fatalf("cleanup claim failed: %+v %v", assets, err)
	}
	if err = second.DeleteCleanedImageRelay(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err = first.ReserveImageRelay(ctx, 1<<30, 512); err != nil {
		t.Fatalf("quota not released: %v", err)
	}
}

func TestImageAssetRelayCleanupRetryFairness(t *testing.T) {
	db, err := New("sqlite", filepath.Join(t.TempDir(), "cleanup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	testImageRelayCleanupRetryFairness(t, db)
}
func testImageRelayCleanupRetryFairness(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	failedIDs := map[int64]bool{}
	for i := 0; i < 512; i++ {
		id, err := db.InsertImageAsset(ctx, ImageAssetInput{Model: "bps-inbound", Bytes: 100})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.conn.ExecContext(ctx, "UPDATE image_assets SET relay_state='ready',relay_expires_at=$1 WHERE id=$2", now.Add(-time.Minute).Unix(), id); err != nil {
			t.Fatal(err)
		}
		if i < 200 {
			failedIDs[id] = true
		}
	}
	first, err := db.ClaimExpiredImageRelay(ctx, now, 200)
	if err != nil || len(first) != 200 {
		t.Fatalf("first claim: count=%d err=%v", len(first), err)
	}
	for _, asset := range first {
		if !failedIDs[asset.ID] {
			t.Fatal("unexpected initial claim")
		}
	}
	// A separate wrapper verifies scheduling is persisted across workers/restarts.
	worker := &DB{conn: db.conn, driver: db.driver}
	removed := 0
	for i := 0; i < 2; i++ {
		assets, err := worker.ClaimExpiredImageRelay(ctx, now, 200)
		if err != nil {
			t.Fatal(err)
		}
		for _, asset := range assets {
			if failedIDs[asset.ID] {
				t.Fatal("failed batch retried before other expired objects")
			}
			if err := worker.DeleteCleanedImageRelay(ctx, asset.ID); err != nil {
				t.Fatal(err)
			}
			removed++
		}
	}
	if removed != 312 {
		t.Fatalf("later objects starved: removed=%d", removed)
	}
	usage, err := db.GetImageRelayUsage(ctx)
	if err != nil || usage.Assets != 200 || usage.Bytes != 20000 || usage.CleanupPending != 200 {
		t.Fatalf("failed objects lost quota: %+v %v", usage, err)
	}
	early, err := worker.ClaimExpiredImageRelay(ctx, now.Add(59*time.Second), 200)
	if err != nil || len(early) != 0 {
		t.Fatalf("immediate retries: count=%d err=%v", len(early), err)
	}
	retry, err := worker.ClaimExpiredImageRelay(ctx, now.Add(time.Minute), 200)
	if err != nil || len(retry) != 200 {
		t.Fatalf("retry unavailable: count=%d err=%v", len(retry), err)
	}
	for _, asset := range retry {
		if err := worker.DeleteCleanedImageRelay(ctx, asset.ID); err != nil {
			t.Fatal(err)
		}
	}
	usage, err = db.GetImageRelayUsage(ctx)
	if err != nil || usage.Assets != 0 || usage.Bytes != 0 {
		t.Fatalf("recovery quota: %+v %v", usage, err)
	}
}
