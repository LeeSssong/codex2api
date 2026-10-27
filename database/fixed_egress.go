package database

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/codex2api/security"
	"os"
	"strings"
	"time"
)

var ErrFixedEgressUnavailable = errors.New("fixed egress unavailable")
var ErrFixedEgressStale = errors.New("stale fixed egress binding")
var ErrFixedEgressSecretKey = errors.New("fixed egress secret key unavailable")

type FixedEgressConfig struct {
	Enabled  bool  `json:"enabled"`
	Revision int64 `json:"revision"`
}
type FixedEgressPool struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Strategy        string    `json:"strategy"`
	CooldownSeconds int       `json:"cooldown_seconds"`
	MaxAttempts     int       `json:"max_attempts"`
	Rotate429       bool      `json:"rotate_429"`
	Retry5xx        bool      `json:"retry_5xx"`
	UpdatedAt       time.Time `json:"updated_at"`
}
type FixedEgressResource struct {
	ID             string     `json:"id"`
	PoolID         string     `json:"pool_id"`
	ProviderID     string     `json:"provider_id"`
	ProviderPort   int        `json:"provider_port"`
	ProxyURL       string     `json:"-"`
	ObservedExitIP string     `json:"observed_exit_ip"`
	State          string     `json:"state"`
	CooldownUntil  *time.Time `json:"cooldown_until,omitempty"`
	Owned          bool       `json:"owned"`
	ProviderTaskID string     `json:"provider_task_id"`
	ChangedAt      time.Time  `json:"changed_at"`
}
type FixedEgressBinding struct {
	AccountID     int64     `json:"account_id"`
	PoolID        string    `json:"pool_id"`
	ResourceID    string    `json:"resource_id"`
	ProxyURL      string    `json:"-"`
	Generation    int64     `json:"generation"`
	State         string    `json:"state"`
	ChangedReason string    `json:"changed_reason"`
	ChangeCount   int64     `json:"change_count"`
	ChangedAt     time.Time `json:"changed_at"`
}
type FixedEgressProvider struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	APIKey                string   `json:"-"`
	BaseURL               string   `json:"base_url"`
	Protocol              string   `json:"protocol"`
	AuthFormat            string   `json:"auth_format"`
	Country               string   `json:"country"`
	City                  string   `json:"city"`
	WhitelistIPs          []string `json:"whitelist_ips"`
	PortStart             int      `json:"port_start"`
	PortEnd               int      `json:"port_end"`
	Capacity              int      `json:"capacity"`
	RefillIntervalSeconds int      `json:"refill_interval_seconds"`
	Enabled               bool     `json:"enabled"`
}
type FixedEgressTask struct {
	ID         string    `json:"id"`
	ProviderID string    `json:"provider_id"`
	Port       int       `json:"port"`
	State      string    `json:"state"`
	Result     string    `json:"result"`
	ResourceID string    `json:"resource_id"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SaveFixedEgressProvider persists provider metadata; API credentials are encrypted.
func (db *DB) SaveFixedEgressProvider(ctx context.Context, p FixedEgressProvider) error {
	key, err := feSeal(p.APIKey)
	if err != nil {
		return err
	}
	_, err = db.conn.ExecContext(ctx, `INSERT INTO fixed_egress_providers(id,name,api_key,base_url,protocol,auth_format,country,city,whitelist_ips,port_start,port_end,capacity,refill_interval_seconds,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(id) DO UPDATE SET name=excluded.name,api_key=excluded.api_key,base_url=excluded.base_url,protocol=excluded.protocol,auth_format=excluded.auth_format,country=excluded.country,city=excluded.city,whitelist_ips=excluded.whitelist_ips,port_start=excluded.port_start,port_end=excluded.port_end,capacity=excluded.capacity,refill_interval_seconds=excluded.refill_interval_seconds,enabled=excluded.enabled,updated_at=CURRENT_TIMESTAMP`, p.ID, p.Name, key, p.BaseURL, p.Protocol, p.AuthFormat, p.Country, p.City, strings.Join(p.WhitelistIPs, ","), p.PortStart, p.PortEnd, p.Capacity, p.RefillIntervalSeconds, p.Enabled)
	return err
}
func (db *DB) GetFixedEgressProvider(ctx context.Context, id string) (FixedEgressProvider, error) {
	var p FixedEgressProvider
	var enc, ips string
	err := db.conn.QueryRowContext(ctx, `SELECT id,name,api_key,base_url,protocol,auth_format,country,city,whitelist_ips,port_start,port_end,capacity,refill_interval_seconds,enabled FROM fixed_egress_providers WHERE id=$1`, id).Scan(&p.ID, &p.Name, &enc, &p.BaseURL, &p.Protocol, &p.AuthFormat, &p.Country, &p.City, &ips, &p.PortStart, &p.PortEnd, &p.Capacity, &p.RefillIntervalSeconds, &p.Enabled)
	if err != nil {
		return p, err
	}
	p.APIKey, err = feOpen(enc)
	p.WhitelistIPs = strings.FieldsFunc(ips, func(r rune) bool { return r == ',' })
	return p, err
}
func (db *DB) MarkFixedEgressFailure(ctx context.Context, account int64, generation int64, resourceID, reason string) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var pool, strategy string
		var cooldown int
		e := tx.QueryRowContext(ctx, `SELECT b.pool_id,p.strategy,p.cooldown_seconds FROM fixed_egress_bindings b JOIN fixed_egress_pools p ON p.id=b.pool_id WHERE b.account_id=$1 AND b.generation=$2 AND b.resource_id=$3`, account, generation, resourceID).Scan(&pool, &strategy, &cooldown)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrFixedEgressStale
		}
		if e != nil {
			return e
		}
		if cooldown < 30 {
			cooldown = 30
		}
		if _, e = tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state='cooldown',cooldown_until=$1,changed_at=CURRENT_TIMESTAMP WHERE id=$2`, time.Now().UTC().Add(time.Duration(cooldown)*time.Second), resourceID); e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `UPDATE fixed_egress_bindings SET state='failed',changed_reason=$1,changed_at=CURRENT_TIMESTAMP WHERE account_id=$2`, reason, account); e != nil {
			return e
		}
		if strategy == "stable" {
			var spare string
			e = tx.QueryRowContext(ctx, `SELECT r.id FROM fixed_egress_resources r WHERE r.pool_id=$1 AND r.state='healthy' AND r.observed_exit_ip<>'' AND NOT EXISTS(SELECT 1 FROM fixed_egress_bindings b JOIN fixed_egress_resources br ON br.id=b.resource_id WHERE b.resource_id=r.id OR br.observed_exit_ip=r.observed_exit_ip) ORDER BY r.id LIMIT 1`, pool).Scan(&spare)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e == nil {
				if _, e = tx.ExecContext(ctx, `UPDATE fixed_egress_bindings SET resource_id=$1,generation=generation+1,state='ready',changed_reason=$2,change_count=change_count+1,changed_at=CURRENT_TIMESTAMP WHERE account_id=$3`, spare, reason, account); e != nil {
					return e
				}
				if _, e = tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state='bound' WHERE id=$1`, spare); e != nil {
					return e
				}
			}
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO fixed_egress_events(id,account_id,kind,detail) SELECT COALESCE(MAX(id),0)+1,$1,'failure',$2 FROM fixed_egress_events`, account, reason)
		return e
	})
}

func feKey() ([]byte, error) {
	s := strings.TrimSpace(os.Getenv("CODEX_FIXED_EGRESS_KEY"))
	if s == "" {
		s = strings.TrimSpace(os.Getenv("ADMIN_SECRET"))
		if s == "" {
			return nil, ErrFixedEgressSecretKey
		}
		s = "codex.fixed-egress.v1:" + s
	}
	h := sha256.Sum256([]byte(s))
	return h[:], nil
}
func feSeal(v string) (string, error) {
	k, e := feKey()
	if e != nil {
		return "", e
	}
	b, _ := aes.NewCipher(k)
	g, _ := cipher.NewGCM(b)
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return "", e
	}
	return base64.RawStdEncoding.EncodeToString(g.Seal(n, n, []byte(v), nil)), nil
}
func feOpen(v string) (string, error) {
	k, e := feKey()
	if e != nil {
		return "", e
	}
	r, e := base64.RawStdEncoding.DecodeString(v)
	if e != nil {
		return "", errors.New("invalid fixed egress secret")
	}
	b, e := aes.NewCipher(k)
	if e != nil {
		return "", e
	}
	g, e := cipher.NewGCM(b)
	if e != nil || len(r) < g.NonceSize() {
		return "", errors.New("invalid fixed egress secret")
	}
	o, e := g.Open(nil, r[:g.NonceSize()], r[g.NonceSize():], nil)
	if e != nil {
		return "", errors.New("fixed egress secret decrypt failed")
	}
	return string(o), nil
}
func fixedEgressSchema(ctx context.Context, db *DB) error {
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS fixed_egress_config(id INTEGER PRIMARY KEY,enabled BOOLEAN NOT NULL DEFAULT FALSE,revision BIGINT NOT NULL DEFAULT 0)`,
		`INSERT INTO fixed_egress_config(id,enabled,revision) VALUES(1,FALSE,0) ON CONFLICT(id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_pools(id TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',enabled BOOLEAN NOT NULL DEFAULT FALSE,strategy TEXT NOT NULL DEFAULT 'strict',cooldown_seconds INTEGER NOT NULL DEFAULT 30,max_attempts INTEGER NOT NULL DEFAULT 3,rotate_429 BOOLEAN NOT NULL DEFAULT FALSE,retry_5xx BOOLEAN NOT NULL DEFAULT FALSE,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_providers(id TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',api_key TEXT NOT NULL DEFAULT '',base_url TEXT NOT NULL DEFAULT '',protocol TEXT NOT NULL DEFAULT '',auth_format TEXT NOT NULL DEFAULT '',country TEXT NOT NULL DEFAULT '',city TEXT NOT NULL DEFAULT '',whitelist_ips TEXT NOT NULL DEFAULT '',port_start INTEGER NOT NULL DEFAULT 0,port_end INTEGER NOT NULL DEFAULT 0,capacity INTEGER NOT NULL DEFAULT 0,refill_interval_seconds INTEGER NOT NULL DEFAULT 0,enabled BOOLEAN NOT NULL DEFAULT FALSE,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_resources(id TEXT PRIMARY KEY,pool_id TEXT NOT NULL REFERENCES fixed_egress_pools(id),provider_id TEXT,provider_port INTEGER NOT NULL DEFAULT 0,proxy_ciphertext TEXT NOT NULL DEFAULT '',observed_exit_ip TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'pending',cooldown_until TIMESTAMP,owned BOOLEAN NOT NULL DEFAULT FALSE,provider_task_id TEXT NOT NULL DEFAULT '',changed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE INDEX IF NOT EXISTS idx_fixed_egress_resources_pool ON fixed_egress_resources(pool_id,state)`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_enrollments(account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,pool_id TEXT NOT NULL REFERENCES fixed_egress_pools(id),created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_bindings(account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,pool_id TEXT NOT NULL REFERENCES fixed_egress_pools(id),resource_id TEXT NOT NULL REFERENCES fixed_egress_resources(id),generation BIGINT NOT NULL DEFAULT 1,state TEXT NOT NULL DEFAULT 'ready',changed_reason TEXT NOT NULL DEFAULT '',change_count BIGINT NOT NULL DEFAULT 0,changed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS uq_fixed_egress_binding_resource ON fixed_egress_bindings(resource_id)`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_events(id INTEGER PRIMARY KEY,account_id BIGINT NOT NULL,kind TEXT NOT NULL,detail TEXT NOT NULL DEFAULT '',created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_leases(id TEXT PRIMARY KEY,resource_id TEXT NOT NULL REFERENCES fixed_egress_resources(id),expires_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS fixed_egress_tasks(id TEXT PRIMARY KEY,provider_id TEXT NOT NULL,port INTEGER NOT NULL,state TEXT NOT NULL,result TEXT NOT NULL DEFAULT '',resource_id TEXT NOT NULL DEFAULT '',created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`} {
		if _, e := db.conn.ExecContext(ctx, s); e != nil {
			return e
		}
	}
	return nil
}
func (db *DB) InitFixedEgress(ctx context.Context) error { return fixedEgressSchema(ctx, db) }
func (db *DB) SaveFixedEgressConfig(ctx context.Context, c FixedEgressConfig) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET enabled=$1,revision=revision+1 WHERE id=1`, c.Enabled)
		return e
	})
}

func (db *DB) SetFixedEgressConfigRevision(ctx context.Context, enabled bool, revision int64) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		r, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET enabled=$1,revision=revision+1 WHERE id=1 AND revision=$2`, enabled, revision)
		if e != nil {
			return e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return ErrFixedEgressStale
		}
		return nil
	})
}
func (db *DB) GetFixedEgressConfig(ctx context.Context) (FixedEgressConfig, error) {
	var c FixedEgressConfig
	e := db.conn.QueryRowContext(ctx, `SELECT enabled,revision FROM fixed_egress_config WHERE id=1`).Scan(&c.Enabled, &c.Revision)
	return c, e
}
func (db *DB) SaveFixedEgressPool(ctx context.Context, p FixedEgressPool) error {
	if p.Strategy != "strict" && p.Strategy != "stable" {
		return fmt.Errorf("invalid strategy")
	}
	_, e := db.conn.ExecContext(ctx, `INSERT INTO fixed_egress_pools(id,name,enabled,strategy,cooldown_seconds,max_attempts,rotate_429,retry_5xx) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET name=excluded.name,enabled=excluded.enabled,strategy=excluded.strategy,cooldown_seconds=excluded.cooldown_seconds,max_attempts=excluded.max_attempts,rotate_429=excluded.rotate_429,retry_5xx=excluded.retry_5xx,updated_at=CURRENT_TIMESTAMP`, p.ID, p.Name, p.Enabled, p.Strategy, p.CooldownSeconds, p.MaxAttempts, p.Rotate429, p.Retry5xx)
	return e
}
func (db *DB) SaveFixedEgressResource(ctx context.Context, r FixedEgressResource) error {
	u, parseErr := security.ParseProxyURL(r.ProxyURL)
	if parseErr != nil || u.Hostname() == "" || u.Port() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid fixed proxy address")
	}
	enc, e := feSeal(r.ProxyURL)
	if e != nil {
		return e
	}
	if r.State == "" {
		r.State = "healthy"
	}
	if r.State == "available" {
		r.State = "healthy"
	}
	_, e = db.conn.ExecContext(ctx, `INSERT INTO fixed_egress_resources(id,pool_id,provider_id,provider_port,proxy_ciphertext,observed_exit_ip,state,cooldown_until,owned,provider_task_id) VALUES($1,$2,NULLIF($3,''),$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(id) DO UPDATE SET pool_id=excluded.pool_id,provider_id=excluded.provider_id,provider_port=excluded.provider_port,proxy_ciphertext=excluded.proxy_ciphertext,observed_exit_ip=excluded.observed_exit_ip,state=excluded.state,cooldown_until=excluded.cooldown_until,owned=excluded.owned,provider_task_id=excluded.provider_task_id,changed_at=CURRENT_TIMESTAMP`, r.ID, r.PoolID, r.ProviderID, r.ProviderPort, enc, r.ObservedExitIP, r.State, r.CooldownUntil, r.Owned, r.ProviderTaskID)
	return e
}
func (db *DB) EnrollFixedEgress(ctx context.Context, a int64, p string) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var n int
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=$1 AND deleted_at IS NULL`, a).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return ErrFixedEgressUnavailable
		}
		var old string
		e := tx.QueryRowContext(ctx, `SELECT pool_id FROM fixed_egress_enrollments WHERE account_id=$1`, a).Scan(&old)
		if e == nil {
			if old != p {
				return ErrFixedEgressStale
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO fixed_egress_enrollments(account_id,pool_id) VALUES($1,$2)`, a, p)
		return e
	})
}
func (db *DB) UnenrollFixedEgress(ctx context.Context, a int64) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state='healthy' WHERE state='bound' AND id IN(SELECT resource_id FROM fixed_egress_bindings WHERE account_id=$1)`, a); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `DELETE FROM fixed_egress_bindings WHERE account_id=$1`, a); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `DELETE FROM fixed_egress_enrollments WHERE account_id=$1`, a)
		return e
	})
}
func (db *DB) EnsureFixedEgressBinding(ctx context.Context, a int64) (FixedEgressBinding, error) {
	var out FixedEgressBinding
	e := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		return db.ensureFixedBindingTx(ctx, tx, a, &out)
	})
	return out, e
}
func (db *DB) ensureFixedBindingTx(ctx context.Context, tx *sql.Tx, a int64, out *FixedEgressBinding) error {
	// Serialize claims across PostgreSQL connections (and make the lock explicit on SQLite).
	if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
		return e
	}
	var on bool
	if e := tx.QueryRowContext(ctx, `SELECT enabled FROM fixed_egress_config WHERE id=1`).Scan(&on); e != nil || !on {
		return ErrFixedEgressUnavailable
	}
	var pool string
	if e := tx.QueryRowContext(ctx, `SELECT e.pool_id FROM fixed_egress_enrollments e JOIN fixed_egress_pools p ON p.id=e.pool_id AND p.enabled WHERE e.account_id=$1`, a).Scan(&pool); e != nil {
		return ErrFixedEgressUnavailable
	}
	var rid, enc, state, resourceState string
	var gen int64
	if e := tx.QueryRowContext(ctx, `SELECT b.resource_id,r.proxy_ciphertext,b.generation,b.state,r.state FROM fixed_egress_bindings b JOIN fixed_egress_resources r ON r.id=b.resource_id WHERE b.account_id=$1`, a).Scan(&rid, &enc, &gen, &state, &resourceState); e == nil {
		if state != "ready" || (resourceState != "healthy" && resourceState != "bound") {
			return ErrFixedEgressUnavailable
		}
		p, de := feOpen(enc)
		if de != nil {
			return de
		}
		*out = FixedEgressBinding{AccountID: a, PoolID: pool, ResourceID: rid, ProxyURL: p, Generation: gen, State: state}
		return nil
	}
	if e := tx.QueryRowContext(ctx, `SELECT r.id,r.proxy_ciphertext FROM fixed_egress_resources r WHERE r.pool_id=$1 AND r.state='healthy' AND r.observed_exit_ip<>'' AND NOT EXISTS(SELECT 1 FROM fixed_egress_bindings b WHERE b.resource_id=r.id) AND NOT EXISTS(SELECT 1 FROM fixed_egress_bindings b JOIN fixed_egress_resources br ON br.id=b.resource_id WHERE br.observed_exit_ip=r.observed_exit_ip) ORDER BY r.id LIMIT 1`, pool).Scan(&rid, &enc); e != nil {
		return ErrFixedEgressUnavailable
	}
	p, de := feOpen(enc)
	if de != nil {
		return de
	}
	if _, e := tx.ExecContext(ctx, `INSERT INTO fixed_egress_bindings(account_id,pool_id,resource_id) VALUES($1,$2,$3)`, a, pool, rid); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state='bound' WHERE id=$1`, rid); e != nil {
		return e
	}
	*out = FixedEgressBinding{AccountID: a, PoolID: pool, ResourceID: rid, ProxyURL: p, Generation: 1, State: "ready"}
	return nil
}
func (db *DB) EnrollAndBindFixedEgress(ctx context.Context, a int64, p string) (FixedEgressBinding, error) {
	var out FixedEgressBinding
	e := db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var n int
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE id=$1 AND deleted_at IS NULL`, a).Scan(&n); e != nil {
			return e
		}
		if n != 1 {
			return ErrFixedEgressUnavailable
		}
		var old string
		e := tx.QueryRowContext(ctx, `SELECT pool_id FROM fixed_egress_enrollments WHERE account_id=$1`, a).Scan(&old)
		if e == nil && old != p {
			return ErrFixedEgressStale
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if errors.Is(e, sql.ErrNoRows) {
			if _, e = tx.ExecContext(ctx, `INSERT INTO fixed_egress_enrollments(account_id,pool_id) VALUES($1,$2)`, a, p); e != nil {
				return e
			}
		}
		return db.ensureFixedBindingTx(ctx, tx, a, &out)
	})
	return out, e
}
func (db *DB) ResolveFixedEgress(ctx context.Context, a int64) (FixedEgressBinding, error) {
	var b FixedEgressBinding
	var enc string
	e := db.conn.QueryRowContext(ctx, `SELECT b.account_id,b.pool_id,b.resource_id,r.proxy_ciphertext,b.generation,b.state,b.changed_reason,b.change_count,b.changed_at FROM fixed_egress_bindings b JOIN fixed_egress_resources r ON r.id=b.resource_id WHERE b.account_id=$1`, a).Scan(&b.AccountID, &b.PoolID, &b.ResourceID, &enc, &b.Generation, &b.State, &b.ChangedReason, &b.ChangeCount, &b.ChangedAt)
	if e != nil {
		return b, e
	}
	b.ProxyURL, e = feOpen(enc)
	return b, e
}
func (db *DB) RotateFixedEgress(ctx context.Context, a, g int64, r string) error {
	return db.withWriteTx(ctx, func(tx *sql.Tx) error {
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_config SET revision=revision WHERE id=1`); e != nil {
			return e
		}
		var pool, current string
		var generation int64
		if e := tx.QueryRowContext(ctx, `SELECT pool_id,resource_id,generation FROM fixed_egress_bindings WHERE account_id=$1`, a).Scan(&pool, &current, &generation); e != nil {
			return e
		}
		if generation != g {
			return ErrFixedEgressStale
		}
		var np, st, ip string
		if e := tx.QueryRowContext(ctx, `SELECT pool_id,state,observed_exit_ip FROM fixed_egress_resources WHERE id=$1`, r).Scan(&np, &st, &ip); e != nil {
			return e
		}
		if np != pool || st != "healthy" || ip == "" || r == current {
			return ErrFixedEgressUnavailable
		}
		var n int
		if e := tx.QueryRowContext(ctx, `SELECT count(*) FROM fixed_egress_bindings b JOIN fixed_egress_resources br ON br.id=b.resource_id WHERE b.resource_id=$1 OR br.observed_exit_ip=$2`, r, ip).Scan(&n); e != nil {
			return e
		}
		if n > 0 {
			return ErrFixedEgressUnavailable
		}
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_bindings SET resource_id=$1,generation=generation+1,state='ready',changed_reason='manual',change_count=change_count+1,changed_at=CURRENT_TIMESTAMP WHERE account_id=$2`, r, a); e != nil {
			return e
		}
		if _, e := tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state='healthy' WHERE id=$1 AND state='bound'`, current); e != nil {
			return e
		}
		_, e := tx.ExecContext(ctx, `UPDATE fixed_egress_resources SET state='bound' WHERE id=$1`, r)
		return e
	})
}
func (db *DB) ListFixedEgress(ctx context.Context) ([]FixedEgressResource, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT id,pool_id,COALESCE(provider_id,''),provider_port,proxy_ciphertext,observed_exit_ip,state,cooldown_until,owned,provider_task_id,changed_at FROM fixed_egress_resources ORDER BY id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []FixedEgressResource{}
	for rows.Next() {
		var r FixedEgressResource
		var enc string
		if e = rows.Scan(&r.ID, &r.PoolID, &r.ProviderID, &r.ProviderPort, &enc, &r.ObservedExitIP, &r.State, &r.CooldownUntil, &r.Owned, &r.ProviderTaskID, &r.ChangedAt); e != nil {
			return nil, e
		}
		r.ProxyURL, e = feOpen(enc)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (db *DB) ListFixedEgressBindings(ctx context.Context) ([]FixedEgressBinding, error) {
	rows, e := db.conn.QueryContext(ctx, `SELECT b.account_id,b.pool_id,b.resource_id,r.proxy_ciphertext,b.generation,b.state,b.changed_reason,b.change_count,b.changed_at FROM fixed_egress_bindings b JOIN fixed_egress_resources r ON r.id=b.resource_id ORDER BY b.account_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []FixedEgressBinding{}
	for rows.Next() {
		var b FixedEgressBinding
		var enc string
		if e = rows.Scan(&b.AccountID, &b.PoolID, &b.ResourceID, &enc, &b.Generation, &b.State, &b.ChangedReason, &b.ChangeCount, &b.ChangedAt); e != nil {
			return nil, e
		}
		b.ProxyURL, e = feOpen(enc)
		if e != nil {
			return nil, e
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
