package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/database"
	"github.com/codex2api/internal/imagestore"
	"github.com/codex2api/internal/signedasset"
)

const (
	// basispointsImageModel tags inbound images in image_assets so the gallery and
	// the retention sweep can tell them apart from generated images.
	basispointsImageModel = "bps-inbound"
	// basispointsImageURLTTL only needs to outlive one upstream request including
	// slow fetches and retries; every turn re-signs a fresh link.
	basispointsImageURLTTL = 6 * time.Hour
	// basispointsImageRetention bounds how long an inbound image stays hosted.
	basispointsImageRetention = 24 * time.Hour
	// basispointsImageReuseWindow lets later turns of the same conversation reuse
	// an asset for identical bytes. Reuse window plus link TTL stays below the
	// retention so a reused link never outlives its file.
	basispointsImageReuseWindow = 12 * time.Hour
	basispointsImageReuseLimit  = 4096
	basispointsImageSweepEvery  = time.Hour
	basispointsImageSweepBatch  = 200
)

type basispointsHostedImage struct {
	assetID  int64
	storedAt time.Time
}

// basispointsImageStore is the built-in host: the configured imagestore backend,
// an image_assets row per stored image and signed /p/img links. Recent digests
// are remembered in-process so a conversation replaying the same image each turn
// does not store it again.
type basispointsImageStore struct {
	db     *database.DB
	now    func() time.Time
	mu     sync.Mutex
	recent map[[sha256.Size]byte]basispointsHostedImage
}

// NewBasispointsImageHost returns the built-in host, or nil with a logged reason
// when signed links cannot be public HTTPS URLs that OpenAI's servers can fetch.
func NewBasispointsImageHost(db *database.DB) *BasispointsImageHost {
	if db == nil {
		return nil
	}
	base := signedasset.PublicBaseURL()
	if !strings.HasPrefix(base, "https://") {
		log.Printf("[Basispoints] image host disabled: IMAGE_ASSET_PUBLIC_BASE_URL must be a public HTTPS base URL reachable by OpenAI's servers; embedded images keep using the original Codex channel")
		return nil
	}
	store := &basispointsImageStore{db: db, now: time.Now, recent: make(map[[sha256.Size]byte]basispointsHostedImage)}
	return &BasispointsImageHost{Host: store.host}
}

// StartBasispointsImageHost installs the built-in host when it can produce public
// links and sweeps expired inbound images until ctx ends. The sweep runs even
// without a host so images hosted by an earlier configuration are still removed.
func StartBasispointsImageHost(ctx context.Context, db *database.DB) {
	if db == nil {
		return
	}
	SetBasispointsImageHost(NewBasispointsImageHost(db))
	go func() {
		ticker := time.NewTicker(basispointsImageSweepEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweepBasispointsImages(ctx, db, time.Now().Add(-basispointsImageRetention))
			}
		}
	}()
}

func (s *basispointsImageStore) host(ctx context.Context, data []byte, mime string) (string, error) {
	sum := sha256.Sum256(data)
	if id, ok := s.reusable(sum); ok {
		return signedasset.ImageAssetURLWithTTL(id, 0, basispointsImageURLTTL), nil
	}
	backend, err := imagestore.Primary()
	if err != nil {
		return "", err
	}
	extension := basispointsImageExtension(mime)
	key := fmt.Sprintf("bps-%d-%s.%s", s.now().UnixNano(), hex.EncodeToString(sum[:6]), extension)
	ref, err := backend.Save(ctx, key, data, mime)
	if err != nil {
		return "", fmt.Errorf("save image: %w", err)
	}
	width, height := inlineImageDimensions(data)
	id, err := s.db.InsertImageAsset(ctx, database.ImageAssetInput{
		Filename: key, StoragePath: ref, MimeType: mime, Bytes: len(data), Width: width, Height: height,
		Model: basispointsImageModel, ActualSize: imageActualSize(width, height), OutputFormat: extension,
	})
	if err != nil {
		_ = backend.Delete(ctx, ref)
		return "", fmt.Errorf("record image asset: %w", err)
	}
	s.remember(sum, id)
	return signedasset.ImageAssetURLWithTTL(id, 0, basispointsImageURLTTL), nil
}

func (s *basispointsImageStore) reusable(sum [sha256.Size]byte) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.recent[sum]
	if !ok {
		return 0, false
	}
	if s.now().Sub(entry.storedAt) > basispointsImageReuseWindow {
		delete(s.recent, sum)
		return 0, false
	}
	return entry.assetID, true
}

func (s *basispointsImageStore) remember(sum [sha256.Size]byte, assetID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if len(s.recent) >= basispointsImageReuseLimit {
		for digest, entry := range s.recent {
			if now.Sub(entry.storedAt) > basispointsImageReuseWindow {
				delete(s.recent, digest)
			}
		}
		for digest := range s.recent {
			if len(s.recent) < basispointsImageReuseLimit {
				break
			}
			delete(s.recent, digest)
		}
	}
	s.recent[sum] = basispointsHostedImage{assetID: assetID, storedAt: now}
}

// sweepBasispointsImages deletes inbound images created before cutoff, row first
// and then the stored object, mirroring the gallery's delete order.
func sweepBasispointsImages(ctx context.Context, db *database.DB, cutoff time.Time) (int, error) {
	removed := 0
	// One sweep clears at most this many batches; the next tick continues.
	for batch := 0; batch < 1000; batch++ {
		assets, err := db.ListImageAssetsByModelBefore(ctx, basispointsImageModel, cutoff, basispointsImageSweepBatch)
		if err != nil {
			log.Printf("[Basispoints] image sweep: list expired assets: %v", err)
			return removed, err
		}
		if len(assets) == 0 {
			if removed > 0 {
				log.Printf("[Basispoints] image sweep removed %d expired inbound images", removed)
			}
			return removed, nil
		}
		for _, asset := range assets {
			if err := db.DeleteImageAsset(ctx, asset.ID); err != nil {
				log.Printf("[Basispoints] image sweep: delete asset %d: %v", asset.ID, err)
				return removed, err
			}
			if asset.StoragePath != "" {
				if backend, err := imagestore.Resolve(asset.StoragePath); err == nil {
					_ = backend.Delete(ctx, asset.StoragePath)
				}
			}
			removed++
		}
	}
	log.Printf("[Basispoints] image sweep stopped after %d expired inbound images; continuing next tick", removed)
	return removed, nil
}

func basispointsImageExtension(mime string) string {
	switch mime {
	case "image/jpeg":
		return "jpg"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	case "image/bmp":
		return "bmp"
	default:
		return "png"
	}
}

func inlineImageDimensions(data []byte) (int, int) {
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		return cfg.Width, cfg.Height
	}
	if width, height, ok := decodeWebPDimensions(data); ok {
		return width, height
	}
	return 0, 0
}
