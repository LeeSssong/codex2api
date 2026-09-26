package proxy

import (
	"context"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/imagestore"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newRelayTestStore(t *testing.T) (*basispointsImageStore, context.Context, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("IMAGE_ASSET_SIGNING_SECRET", "persistent-test-secret")
	if err := imagestore.Configure(imagestore.Config{Backend: "local", LocalDir: dir}); err != nil {
		t.Fatal(err)
	}
	db, err := database.New("sqlite", filepath.Join(t.TempDir(), "images.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := WithBasispointsImageScope(context.Background(), "tenant")
	if err = db.SaveBasispointsSettings(ctx, database.BasispointsSettings{Enabled: true, ModelScope: "all", ImageRelayEnabled: true, ImageRelayPublicOrigin: "https://relay.example"}); err != nil {
		t.Fatal(err)
	}
	return &basispointsImageStore{db: db}, ctx, dir
}
func TestBasispointsImageFailedCleanupStaysChargedAndRetries(t *testing.T) {
	s, ctx, dir := newRelayTestStore(t)
	png := solidPNG(t, 4, color.RGBA{A: 255})
	reserved, finish, err := s.begin(ctx, int64(len(png)), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.host(reserved, png, "image/png"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("missing stored object")
	}
	file := filepath.Join(dir, entries[0].Name())
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	// An undeletable nonempty directory simulates object deletion failure.
	if err = os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(file, "keep"), []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = finish(false); err != nil {
		t.Fatal(err)
	}
	usage, err := s.db.GetImageRelayUsage(ctx)
	if err != nil || usage.Bytes != int64(len(png)) || usage.CleanupPending != 1 || usage.CleanupErrors == 0 {
		t.Fatalf("failed cleanup lost metadata/quota: %+v %v", usage, err)
	}
	if err = os.Remove(filepath.Join(file, "keep")); err != nil {
		t.Fatal(err)
	}
	if n, err := sweepBasispointsImages(ctx, s.db, time.Now()); err != nil || n != 1 {
		t.Fatalf("cleanup retry failed: %d %v", n, err)
	}
	usage, _ = s.db.GetImageRelayUsage(ctx)
	if usage.Bytes != 0 || usage.Assets != 0 {
		t.Fatalf("successful cleanup retained quota: %+v", usage)
	}
}
func TestBasispointsImageWriteFailureReleasesWholeReservation(t *testing.T) {
	s, ctx, dir := newRelayTestStore(t)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	_, err := s.host(ctx, solidPNG(t, 4, color.RGBA{A: 255}), "image/png")
	if err == nil {
		t.Fatal("missing storage directory accepted")
	}
	usage, err := s.db.GetImageRelayUsage(ctx)
	if err != nil || usage.Bytes != 0 || usage.ReservedBytes != 0 || usage.Assets != 0 {
		t.Fatalf("failed write leaked: %+v %v", usage, err)
	}
}
func TestBasispointsImageDisableDuringConversionRollsBack(t *testing.T) {
	s, ctx, _ := newRelayTestStore(t)
	png := solidPNG(t, 4, color.RGBA{A: 255})
	reserved, finish, err := s.begin(ctx, int64(len(png)), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.host(reserved, png, "image/png"); err != nil {
		t.Fatal(err)
	}
	settings, _ := s.db.GetBasispointsSettings(ctx)
	settings.ImageRelayEnabled = false
	if err = s.db.SaveBasispointsSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err = finish(true); err == nil {
		t.Fatal("conversion committed after hot disable")
	}
	if err = finish(false); err != nil {
		t.Fatal(err)
	}
	usage, _ := s.db.GetImageRelayUsage(ctx)
	if usage.Assets != 0 || usage.ReservedBytes != 0 {
		t.Fatalf("disabled conversion leaked: %+v", usage)
	}
}
func TestBasispointsImageConcurrentDuplicateNeverOverwrites(t *testing.T) {
	s, ctx, dir := newRelayTestStore(t)
	png := solidPNG(t, 4, color.RGBA{A: 255})
	a, af, err := s.begin(ctx, int64(len(png)), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer af(false)
	first, err := s.host(a, png, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	b, bf, err := s.begin(ctx, int64(len(png)), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer bf(false)
	if _, err = s.host(b, png, "image/png"); err == nil {
		t.Fatal("uncommitted duplicate must fail fast")
	}
	if err = af(true); err != nil {
		t.Fatal(err)
	}
	second, err := s.host(b, png, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if err = bf(true); err != nil {
		t.Fatal(err)
	}
	if strings.Split(first, "?")[0] != strings.Split(second, "?")[0] {
		t.Fatal("duplicate received a second asset")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("duplicate storage objects: %d", len(entries))
	}
}
