package proxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/codex2api/database"
	"github.com/codex2api/internal/imagestore"
	"github.com/codex2api/internal/signedasset"
	"image"
	"time"
)

const (
	basispointsImageModel       = "bps-inbound"
	basispointsImageURLTTL      = database.ImageRelayLifetime
	basispointsImageRetention   = database.ImageRelayLifetime
	basispointsImageReuseWindow = database.ImageRelayLifetime
	basispointsImageSweepEvery  = time.Minute
	basispointsImageSweepBatch  = 200
)

type basispointsImageStore struct{ db *database.DB }
type basispointsImageTransactionKey struct{}
type basispointsImageTransaction struct {
	token, scope, origin string
	epoch                int64
	reused               []int64
	expires              time.Time
}

func (s *basispointsImageStore) settings(ctx context.Context) (database.BasispointsSettings, error) {
	settings, err := s.db.GetBasispointsSettings(ctx)
	if err != nil {
		return settings, err
	}
	if !settings.Enabled || !settings.ImageRelayEnabled || !signedasset.PersistentSigningConfigured() {
		return settings, errBasispointsImageHostUnavailable
	}
	origin, err := signedasset.HTTPSOrigin(settings.ImageRelayPublicOrigin)
	if err != nil {
		return settings, errBasispointsImageHostUnavailable
	}
	settings.ImageRelayPublicOrigin = origin
	return settings, nil
}

// Install even while disabled, so later saved settings take effect immediately.
func NewBasispointsImageHost(db *database.DB) *BasispointsImageHost {
	if db == nil {
		return nil
	}
	s := &basispointsImageStore{db: db}
	return &BasispointsImageHost{Host: s.host, Begin: s.begin, Available: func(ctx context.Context) bool { _, err := s.settings(ctx); return err == nil }}
}
func StartBasispointsImageHost(ctx context.Context, db *database.DB) {
	if db == nil {
		return
	}
	SetBasispointsImageHost(NewBasispointsImageHost(db))
	go func() {
		ticker := time.NewTicker(basispointsImageSweepEvery)
		defer ticker.Stop()
		for {
			_, _ = sweepBasispointsImages(ctx, db, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
func (s *basispointsImageStore) begin(ctx context.Context, size int64, count int) (context.Context, func(bool) error, error) {
	settings, err := s.settings(ctx)
	if err != nil {
		return ctx, nil, err
	}
	token, err := s.db.ReserveImageRelay(ctx, size, count)
	if err != nil {
		return ctx, nil, imageInputError(503, "Basispoints image storage capacity unavailable")
	}
	scope := basispointsImageScope(ctx)
	if scope == "" {
		sum := sha256.Sum256([]byte(token))
		scope = hex.EncodeToString(sum[:])
	}
	tx := &basispointsImageTransaction{token: token, scope: scope, origin: settings.ImageRelayPublicOrigin, epoch: settings.ImageRelayEpoch, expires: time.Now().Add(basispointsImageURLTTL)}
	ctx, cancelRequest := context.WithTimeout(ctx, 2*time.Minute)
	finished := false
	finish := func(commit bool) error {
		if finished {
			return nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if commit {
			current, e := s.settings(ctx)
			if e != nil || current.ImageRelayEpoch != tx.epoch || current.ImageRelayPublicOrigin != tx.origin {
				return imageInputError(503, "Basispoints image relay configuration changed")
			}
			if e = ctx.Err(); e != nil {
				return e
			}
		}
		if e := s.db.FinishImageRelay(cleanupCtx, tx.token, commit, tx.reused, tx.epoch); e != nil {
			return imageInputError(503, "Basispoints image lifecycle commit unavailable")
		}
		finished = true
		cancelRequest()
		if !commit {
			_, _ = sweepBasispointsImages(cleanupCtx, s.db, time.Now())
		}
		return nil
	}
	return context.WithValue(ctx, basispointsImageTransactionKey{}, tx), finish, nil
}
func (s *basispointsImageStore) host(ctx context.Context, data []byte, mime string) (string, error) {
	tx, _ := ctx.Value(basispointsImageTransactionKey{}).(*basispointsImageTransaction)
	if tx == nil {
		next, finish, err := s.begin(ctx, int64(len(data)), 1)
		if err != nil {
			return "", err
		}
		ok := false
		defer func() {
			if !ok {
				_ = finish(false)
			}
		}()
		link, err := s.host(next, data, mime)
		if err != nil {
			return "", err
		}
		if err = finish(true); err != nil {
			return "", err
		}
		ok = true
		return link, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	backend, err := imagestore.Primary()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return "", err
	}
	extension := basispointsImageExtension(mime)
	key := "bps-" + hex.EncodeToString(nonce[:]) + "." + extension
	ref, err := imagestore.PlannedRef(backend, key)
	if err != nil {
		return "", err
	}
	width, height := inlineImageDimensions(data)
	id, reused, err := s.db.PrepareImageRelay(ctx, tx.token, tx.scope, hex.EncodeToString(sum[:]), tx.epoch, database.ImageAssetInput{Filename: key, StoragePath: ref, MimeType: mime, Bytes: len(data), Width: width, Height: height, Model: basispointsImageModel, ActualSize: imageActualSize(width, height), OutputFormat: extension})
	if err != nil {
		return "", err
	}
	if reused {
		tx.reused = append(tx.reused, id)
	} else {
		saveCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		saved, e := backend.Save(saveCtx, key, data, mime)
		if e != nil {
			return "", e
		}
		if saved != ref {
			return "", errors.New("image backend changed its planned reference")
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return signedasset.ImageAssetURLAtOrigin(id, tx.origin, tx.expires), nil
}

// Object first, metadata last. An unavailable backend or failed unlink leaves
// quota-accounted deletion metadata for the next startup/tick to retry.
func sweepBasispointsImages(ctx context.Context, db *database.DB, now time.Time) (int, error) {
	assets, err := db.ClaimExpiredImageRelay(ctx, now, basispointsImageSweepBatch)
	if err != nil {
		return 0, err
	}
	removed := 0
	var first error
	for _, asset := range assets {
		backend, e := imagestore.Resolve(asset.StoragePath)
		if e == nil {
			e = backend.Delete(ctx, asset.StoragePath)
		}
		if e == nil {
			e = db.DeleteCleanedImageRelay(ctx, asset.ID)
		}
		if e != nil {
			db.ImageRelayCleanupFailed(ctx)
			observeBasispointsOps(0, 0, "image_cleanup_failed")
			if first == nil {
				first = errors.New("image relay cleanup pending retry")
			}
			continue
		}
		removed++
	}
	return removed, first
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
