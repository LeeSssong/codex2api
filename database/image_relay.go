package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

const ImageRelayMaxBytes int64 = 1 << 30
const ImageRelayMaxAssets int64 = 512
const ImageRelayLifetime = 30 * time.Minute

// Persisted at claim time so failed deletes and interrupted workers retry fairly.
const imageRelayCleanupRetryDelay = time.Minute

var ErrImageRelayCapacity = errors.New("image relay storage capacity exhausted")
var ErrImageRelayBusy = errors.New("image relay asset is being written or removed")

func relayToken() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func (db *DB) ensureImageRelaySchema(ctx context.Context) error {
	for _, c := range []struct{ name, def string }{{"relay_expires_at", "BIGINT NOT NULL DEFAULT 0"}, {"relay_scope_hash", "TEXT NOT NULL DEFAULT ''"}, {"relay_epoch", "BIGINT NOT NULL DEFAULT 0"}, {"relay_digest", "TEXT NOT NULL DEFAULT ''"}, {"relay_state", "TEXT NOT NULL DEFAULT ''"}, {"relay_request", "TEXT NOT NULL DEFAULT ''"}, {"relay_cleanup_next_attempt", "BIGINT NOT NULL DEFAULT 0"}} {
		var e error
		if db.isSQLite() {
			e = db.ensureSQLiteColumn(ctx, "image_assets", c.name, c.def)
		} else {
			_, e = db.conn.ExecContext(ctx, "ALTER TABLE image_assets ADD COLUMN IF NOT EXISTS "+c.name+" "+c.def)
		}
		if e != nil {
			return e
		}
	}
	for _, q := range []string{
		"CREATE TABLE IF NOT EXISTS image_relay_lock (id INTEGER PRIMARY KEY, revision BIGINT NOT NULL DEFAULT 0, rejections BIGINT NOT NULL DEFAULT 0, cleanup_errors BIGINT NOT NULL DEFAULT 0)",
		"INSERT INTO image_relay_lock (id) VALUES (1) ON CONFLICT DO NOTHING",
		"CREATE TABLE IF NOT EXISTS image_relay_reservations (token TEXT PRIMARY KEY, bytes BIGINT NOT NULL, assets BIGINT NOT NULL, expires_at BIGINT NOT NULL)",
		"CREATE INDEX IF NOT EXISTS idx_image_relay_expiry ON image_assets(model,relay_expires_at)",
		"CREATE UNIQUE INDEX IF NOT EXISTS idx_image_relay_identity ON image_assets(relay_scope_hash,relay_epoch,relay_digest) WHERE model='bps-inbound' AND relay_digest<>''",
	} {
		if _, e := db.conn.ExecContext(ctx, q); e != nil {
			return e
		}
	}
	return nil
}

// The singleton UPDATE serializes quota decisions in the database, across all
// workers. Storage I/O always runs outside this short transaction.
func (db *DB) lockImageRelay(ctx context.Context) (*sql.Tx, error) {
	tx, e := db.conn.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, "UPDATE image_relay_lock SET revision=revision+1 WHERE id=1"); e != nil {
		tx.Rollback()
		return nil, e
	}
	return tx, nil
}

type ImageRelayUsage struct {
	Bytes          int64 "json:\"bytes\""
	Assets         int64 "json:\"assets\""
	ReservedBytes  int64 "json:\"reserved_bytes\""
	ReservedAssets int64 "json:\"reserved_assets\""
	CleanupPending int64 "json:\"cleanup_pending\""
	Rejections     int64 "json:\"rejections\""
	CleanupErrors  int64 "json:\"cleanup_errors\""
	MaxBytes       int64 "json:\"max_bytes\""
	MaxAssets      int64 "json:\"max_assets\""
}

func (db *DB) GetImageRelayUsage(ctx context.Context) (ImageRelayUsage, error) {
	u := ImageRelayUsage{MaxBytes: ImageRelayMaxBytes, MaxAssets: ImageRelayMaxAssets}
	e := db.conn.QueryRowContext(ctx, "SELECT COALESCE(SUM(bytes),0),COUNT(*),COALESCE(SUM(CASE WHEN relay_state='deleting' THEN 1 ELSE 0 END),0) FROM image_assets WHERE model='bps-inbound'").Scan(&u.Bytes, &u.Assets, &u.CleanupPending)
	if e != nil {
		return u, e
	}
	e = db.conn.QueryRowContext(ctx, "SELECT COALESCE(SUM(bytes),0),COALESCE(SUM(assets),0) FROM image_relay_reservations").Scan(&u.ReservedBytes, &u.ReservedAssets)
	if e != nil {
		return u, e
	}
	e = db.conn.QueryRowContext(ctx, "SELECT rejections,cleanup_errors FROM image_relay_lock WHERE id=1").Scan(&u.Rejections, &u.CleanupErrors)
	return u, e
}

// Reservations, pending writes, ready assets and failed deletions all count.
func (db *DB) ReserveImageRelay(ctx context.Context, bytes int64, count int) (string, error) {
	if bytes < 0 || count < 0 || bytes > ImageRelayMaxBytes || int64(count) > ImageRelayMaxAssets {
		return "", ErrImageRelayCapacity
	}
	tx, e := db.lockImageRelay(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	var used, n, reserved, rn int64
	if e = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(bytes),0),COUNT(*) FROM image_assets WHERE model='bps-inbound'").Scan(&used, &n); e != nil {
		return "", e
	}
	if e = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(bytes),0),COALESCE(SUM(assets),0) FROM image_relay_reservations").Scan(&reserved, &rn); e != nil {
		return "", e
	}
	if used+reserved+bytes > ImageRelayMaxBytes || n+rn+int64(count) > ImageRelayMaxAssets {
		_, _ = tx.ExecContext(ctx, "UPDATE image_relay_lock SET rejections=rejections+1 WHERE id=1")
		if e = tx.Commit(); e != nil {
			return "", e
		}
		return "", ErrImageRelayCapacity
	}
	token := relayToken()
	_, e = tx.ExecContext(ctx, "INSERT INTO image_relay_reservations(token,bytes,assets,expires_at) VALUES($1,$2,$3,$4)", token, bytes, count, time.Now().Add(ImageRelayLifetime).Unix())
	if e != nil {
		return "", e
	}
	return token, tx.Commit()
}

// Persist object location BEFORE writing; crashes never lose cleanup metadata.
func (db *DB) PrepareImageRelay(ctx context.Context, token, scope, digest string, epoch int64, input ImageAssetInput) (int64, bool, error) {
	tx, e := db.lockImageRelay(ctx)
	if e != nil {
		return 0, false, e
	}
	defer tx.Rollback()
	var id, expires int64
	var state string
	e = tx.QueryRowContext(ctx, "SELECT id,relay_state,relay_expires_at FROM image_assets WHERE model='bps-inbound' AND relay_scope_hash=$1 AND relay_digest=$2 AND relay_epoch=$3", scope, digest, epoch).Scan(&id, &state, &expires)
	if e == nil {
		if state != "ready" || expires <= time.Now().Unix() {
			return 0, false, ErrImageRelayBusy
		}
		return id, true, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return 0, false, e
	}
	r, e := tx.ExecContext(ctx, "UPDATE image_relay_reservations SET bytes=bytes-$1,assets=assets-1 WHERE token=$2 AND bytes >= $1 AND assets>=1 AND expires_at>$3", input.Bytes, token, time.Now().Unix())
	if e != nil {
		return 0, false, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return 0, false, ErrImageRelayCapacity
	}
	q := "INSERT INTO image_assets(filename,storage_path,mime_type,bytes,width,height,model,actual_size,output_format,relay_expires_at,relay_scope_hash,relay_epoch,relay_digest,relay_state,relay_request) VALUES($1,$2,$3,$4,$5,$6,'bps-inbound',$7,$8,$9,$10,$11,$12,'pending',$13) RETURNING id"
	e = tx.QueryRowContext(ctx, q, input.Filename, input.StoragePath, input.MimeType, input.Bytes, input.Width, input.Height, input.ActualSize, input.OutputFormat, time.Now().Add(ImageRelayLifetime).Unix(), scope, epoch, digest, token).Scan(&id)
	if e != nil {
		return 0, false, e
	}
	return id, false, tx.Commit()
}
func (db *DB) FinishImageRelay(ctx context.Context, token string, commit bool, reused []int64, epoch int64) error {
	tx, e := db.lockImageRelay(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	state := "deleting"
	expires := time.Now().Unix()
	if commit {
		state = "ready"
		expires = time.Now().Add(ImageRelayLifetime).Unix()
	}
	if _, e = tx.ExecContext(ctx, "UPDATE image_assets SET relay_state=$1,relay_expires_at=$2 WHERE relay_request=$3 AND relay_state='pending'", state, expires, token); e != nil {
		return e
	}
	if commit {
		for _, id := range reused {
			r, e := tx.ExecContext(ctx, "UPDATE image_assets SET relay_expires_at=$1 WHERE id=$2 AND model='bps-inbound' AND relay_state='ready' AND relay_epoch=$3 AND relay_expires_at>$4", expires, id, epoch, time.Now().Unix())
			if e != nil {
				return e
			}
			n, _ := r.RowsAffected()
			if n != 1 {
				return ErrImageRelayBusy
			}
		}
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM image_relay_reservations WHERE token=$1", token); e != nil {
		return e
	}
	return tx.Commit()
}

// Claim first prevents reuse racing deletion; errors leave rows for retry.
// A failed batch becomes eligible after a bounded delay, while other expired
// objects remain claimable. Order by due time rather than ID so old failures
// cannot monopolize every batch and newly arriving work cannot jump the queue.
func (db *DB) ClaimExpiredImageRelay(ctx context.Context, now time.Time, limit int) ([]ImageAsset, error) {
	tx, e := db.lockImageRelay(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "DELETE FROM image_relay_reservations WHERE expires_at<=$1", now.Unix()); e != nil {
		return nil, e
	}
	rows, e := tx.QueryContext(ctx, "SELECT id FROM image_assets WHERE model='bps-inbound' AND (relay_expires_at <= $1 OR relay_state='deleting') AND relay_cleanup_next_attempt <= $1 ORDER BY CASE WHEN relay_cleanup_next_attempt=0 THEN relay_expires_at ELSE relay_cleanup_next_attempt END,id LIMIT $2", now.Unix(), limit)
	if e != nil {
		return nil, e
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, id := range ids {
		if _, e = tx.ExecContext(ctx, "UPDATE image_assets SET relay_state='deleting',relay_cleanup_next_attempt=$1 WHERE id=$2", now.Add(imageRelayCleanupRetryDelay).Unix(), id); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	var out []ImageAsset
	for _, id := range ids {
		a, e := db.GetImageAsset(ctx, id)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return nil, e
		}
		out = append(out, *a)
	}
	return out, nil
}
func (db *DB) ImageRelayCleanupFailed(ctx context.Context) {
	_, _ = db.conn.ExecContext(ctx, "UPDATE image_relay_lock SET cleanup_errors=cleanup_errors+1 WHERE id=1")
}
func (db *DB) DeleteCleanedImageRelay(ctx context.Context, id int64) error {
	_, e := db.conn.ExecContext(ctx, "DELETE FROM image_assets WHERE id=$1 AND model='bps-inbound' AND relay_state='deleting'", id)
	return e
}

func (db *DB) MarkImageRelayDeleting(ctx context.Context, id int64) error {
	tx, err := db.lockImageRelay(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "UPDATE image_assets SET relay_state='deleting',relay_expires_at=$1 WHERE id=$2 AND model='bps-inbound'", time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}
