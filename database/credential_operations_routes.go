package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/codex2api/credentialops"
)

func (db *DB) freezeCredentialOpsRouteTx(ctx context.Context, tx *sql.Tx, cfg *CredentialOpsLoginConfigRow) error {
	proxy := cfg.ResolvedProxyURL
	if cfg.AccountID > 0 {
		if e := tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT control_revision FROM accounts WHERE id=$1"), cfg.AccountID).Scan(&cfg.RouteRevision); e != nil {
			return e
		}
		var fixedEnabled bool
		err := tx.QueryRowContext(ctx, "SELECT enabled,revision FROM fixed_egress_config WHERE id=1").Scan(&fixedEnabled, &cfg.FixedConfigRevision)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		var enrolled int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM fixed_egress_enrollments WHERE account_id=$1", cfg.AccountID).Scan(&enrolled); err != nil {
			return err
		}
		if fixedEnabled && enrolled > 0 {
			var cipher, resourceState, state string
			if err = tx.QueryRowContext(ctx, "SELECT b.generation,b.resource_id,r.proxy_ciphertext,b.state,r.state FROM fixed_egress_bindings b JOIN fixed_egress_resources r ON r.id=b.resource_id WHERE b.account_id=$1", cfg.AccountID).Scan(&cfg.FixedGeneration, &cfg.FixedResource, &cipher, &state, &resourceState); err != nil {
				return errors.New("fixed egress binding unavailable")
			}
			if state != "ready" || (resourceState != "bound" && resourceState != "healthy") {
				return errors.New("fixed egress binding unavailable")
			}
			if cfg.ProxySource != "account" {
				return errors.New("fixed egress binding conflicts with login proxy choice")
			}
			proxy, err = feOpen(cipher)
			if err != nil {
				return err
			}
			cfg.RouteResolved = true
		}
		if !cfg.RouteResolved && cfg.ProxySource == "account" {
			if err = tx.QueryRowContext(ctx, "SELECT COALESCE(proxy_url,'') FROM accounts WHERE id=$1", cfg.AccountID).Scan(&proxy); err != nil {
				return err
			}
		}
	}
	if cfg.ProxySource == "proxy" {
		if e := tx.QueryRowContext(ctx, "SELECT url FROM proxies WHERE id=$1 AND enabled=TRUE", cfg.ProxyID).Scan(&proxy); e != nil {
			return errors.New("selected proxy is unavailable")
		}
	}
	if cfg.ProxySource == "direct" {
		proxy = ""
	}
	enc, _ := credentialops.EnvCrypto()
	sealed, e := enc(proxy)
	if e != nil {
		return e
	}
	cfg.ProxyURLCiphertext = sealed
	cfg.ResolvedProxyURL = ""
	cfg.RouteResolved = true
	return nil
}
func (db *DB) validateCredentialOpsRouteTx(ctx context.Context, tx *sql.Tx, cfg CredentialOpsLoginConfigRow) error {
	if cfg.ProxySource == "proxy" {
		var current string
		if e := tx.QueryRowContext(ctx, "SELECT url FROM proxies WHERE id=$1 AND enabled=TRUE", cfg.ProxyID).Scan(&current); e != nil {
			return ErrCredentialOpsStale
		}
		_, dec := credentialops.EnvCrypto()
		frozen, e := dec(cfg.ProxyURLCiphertext)
		if e != nil {
			return e
		}
		if current != frozen {
			return ErrCredentialOpsStale
		}
	}
	if cfg.AccountID == 0 {
		return nil
	}
	var revision int64
	if e := tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT control_revision FROM accounts WHERE id=$1"), cfg.AccountID).Scan(&revision); e != nil {
		return e
	}
	if revision != cfg.RouteRevision {
		return ErrCredentialOpsStale
	}
	var fixedRevision int64
	if e := tx.QueryRowContext(ctx, db.credentialOpsLockQuery("SELECT revision FROM fixed_egress_config WHERE id=1")).Scan(&fixedRevision); e != nil && e != sql.ErrNoRows {
		return e
	}
	if fixedRevision != cfg.FixedConfigRevision {
		return ErrCredentialOpsStale
	}
	if cfg.FixedGeneration > 0 {
		var current int64
		var resource, state, resourceState string
		if e := tx.QueryRowContext(ctx, "SELECT b.generation,b.resource_id,b.state,r.state FROM fixed_egress_bindings b JOIN fixed_egress_resources r ON r.id=b.resource_id WHERE b.account_id=$1", cfg.AccountID).Scan(&current, &resource, &state, &resourceState); e != nil {
			return ErrCredentialOpsStale
		}
		if current != cfg.FixedGeneration || resource != cfg.FixedResource || state != "ready" || (resourceState != "bound" && resourceState != "healthy") {
			return ErrCredentialOpsStale
		}
	}
	return nil
}
func credentialOpsLeaseID(id int64, attempt int) string {
	return fmt.Sprintf("credential-operations:%d:%d", id, attempt)
}
func (db *DB) ReleaseCredentialOpsWorkerRoute(ctx context.Context, id int64, owner string, attempt int) error {
	var current string
	e := db.conn.QueryRowContext(ctx, "SELECT owner FROM credential_operations_attempts WHERE task_id=$1 AND attempt=$2", id, attempt).Scan(&current)
	if e != nil {
		return e
	}
	if current != owner {
		return ErrCredentialOpsStale
	}
	return db.ReleaseFixedEgressLease(ctx, credentialOpsLeaseID(id, attempt))
}

func (db *DB) SetCredentialOpsAttemptProxy(ctx context.Context, id int64, owner string, attempt int, proxy string) error {
	enc, _ := credentialops.EnvCrypto()
	cipher, e := enc(proxy)
	if e != nil {
		return e
	}
	result, e := db.conn.ExecContext(ctx, "UPDATE credential_operations_attempts SET proxy_ciphertext=$1 WHERE task_id=$2 AND attempt=$3 AND owner=$4 AND EXISTS(SELECT 1 FROM credential_operations_tasks WHERE id=$2 AND attempt=$3 AND lease_owner=$4 AND status='running' AND lease_until>CURRENT_TIMESTAMP)", cipher, id, attempt, owner)
	if e != nil {
		return e
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrCredentialOpsStale
	}
	return nil
}
