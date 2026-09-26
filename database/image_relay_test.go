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
