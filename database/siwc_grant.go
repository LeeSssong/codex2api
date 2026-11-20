package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/codex2api/credentialops"
)

var ErrSIWCGrantChanged = errors.New("SIWC grant or parent account changed")

type SIWCWorkerFence struct {
	TaskID  int64
	Owner   string
	Attempt int
}

type siwcCommitFence struct {
	worker   *SIWCWorkerFence
	identity *AccountRow
}

func (db *DB) SaveSIWCRefreshedGrant(ctx context.Context, account *AccountRow, grantGeneration int64, credentials map[string]any) error {
	if account == nil || grantGeneration < 1 {
		return ErrSIWCGrantChanged
	}
	return db.saveSIWCGrant(ctx, account.ID, account.CredentialGeneration, grantGeneration, credentials, false, siwcCommitFence{identity: account})
}

func (db *DB) CommitSIWCWorkerGrant(ctx context.Context, accountID, accountGeneration, grantGeneration int64, credentials map[string]any, rebind bool, worker SIWCWorkerFence) error {
	return db.saveSIWCGrant(ctx, accountID, accountGeneration, grantGeneration, credentials, rebind, siwcCommitFence{worker: &worker})
}

func (db *DB) SaveSIWCGrant(ctx context.Context, accountID, accountGeneration, grantGeneration int64, credentials map[string]any) error {
	return db.saveSIWCGrant(ctx, accountID, accountGeneration, grantGeneration, credentials, false)
}

func (db *DB) RebindInvalidatedSIWCGrant(ctx context.Context, accountID, accountGeneration, grantGeneration int64, credentials map[string]any) error {
	if grantGeneration < 1 {
		return ErrSIWCGrantChanged
	}
	return db.saveSIWCGrant(ctx, accountID, accountGeneration, grantGeneration, credentials, true)
}

func (db *DB) saveSIWCGrant(ctx context.Context, accountID, accountGeneration, grantGeneration int64, credentials map[string]any, rebind bool, fences ...siwcCommitFence) error {
	if len(credentials) == 0 || grantGeneration < 0 {
		return errors.New("invalid SIWC grant")
	}
	raw, err := json.Marshal(credentials)
	if err != nil {
		return err
	}
	encrypt, _ := credentialops.EnvCrypto()
	ciphertext, err := encrypt(string(raw))
	if err != nil {
		return err
	}
	return db.withCredentialOpsDurableTx(ctx, func(tx *sql.Tx) error {
		var fence siwcCommitFence
		if len(fences) > 0 {
			fence = fences[0]
		}
		if fence.worker != nil {
			worker := fence.worker
			enabled, err := db.credentialOperationsEnabledTx(ctx, tx)
			if err != nil {
				return err
			}
			if !enabled {
				return ErrCredentialOpsDisabled
			}
			var revision, current int64
			if err := tx.QueryRowContext(ctx, db.credentialOpsLockQuery(`SELECT config_revision FROM credential_operations_tasks WHERE id=$1`), worker.TaskID).Scan(&revision); err != nil {
				return ErrCredentialOpsStale
			}
			if err := tx.QueryRowContext(ctx, db.credentialOpsLockQuery(`SELECT revision FROM credential_operations_login_configs WHERE account_id=$1`), accountID).Scan(&current); err != nil || revision != current {
				return ErrCredentialOpsStale
			}
			cfg, err := credentialOpsConfigInTx(ctx, tx, worker.TaskID)
			if err != nil {
				return err
			}
			if err := db.validateCredentialOpsRouteTx(ctx, tx, cfg); err != nil {
				return err
			}
			result, err := tx.ExecContext(ctx, `UPDATE credential_operations_tasks SET status='succeeded',stage='succeeded',lease_owner='',lease_until=NULL,finished_at=CURRENT_TIMESTAMP,updated_at=CURRENT_TIMESTAMP WHERE id=$1 AND account_id=$2 AND expected_generation=$3 AND lease_owner=$4 AND attempt=$5 AND oauth_profile='siwc' AND status='running' AND lease_until>CURRENT_TIMESTAMP`, worker.TaskID, accountID, accountGeneration, worker.Owner, worker.Attempt)
			if err != nil {
				return err
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if changed != 1 {
				return ErrCredentialOpsStale
			}
		}
		var generation int64
		var raw any
		var proxyURL string
		query := db.credentialOpsLockQuery(`SELECT credential_generation,credentials,proxy_url FROM accounts WHERE id=$1 AND platform='openai' AND type='oauth' AND status<>'deleted'`)
		err := tx.QueryRowContext(ctx, query, accountID).Scan(&generation, &raw, &proxyURL)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSIWCGrantChanged
		}
		if err != nil {
			return err
		}
		if fence.identity != nil {
			current := AccountRow{Credentials: decodeCredentials(raw), ProxyURL: proxyURL}
			if current.ProxyURL != fence.identity.ProxyURL {
				return ErrSIWCGrantChanged
			}
			for _, key := range []string{"email", "workspace_id", "account_id", "user_id", "chatgpt_user_id", "ipv6_egress_enabled", "workstation"} {
				left, _ := json.Marshal(current.Credentials[key])
				right, _ := json.Marshal(fence.identity.Credentials[key])
				if string(left) != string(right) {
					return ErrSIWCGrantChanged
				}
			}
		} else if generation != accountGeneration {
			return ErrSIWCGrantChanged
		}
		var result sql.Result
		if grantGeneration == 0 {
			result, err = tx.ExecContext(ctx, `INSERT INTO credential_operations_profile_grants(account_id,profile,credentials_ciphertext) VALUES($1,'siwc',$2) ON CONFLICT(account_id,profile) DO NOTHING`, accountID, ciphertext)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE credential_operations_profile_grants SET credentials_ciphertext=$1,generation=generation+1,invalidated=FALSE,refresh_pending=FALSE,updated_at=CURRENT_TIMESTAMP WHERE account_id=$2 AND profile='siwc' AND generation=$3 AND invalidated=$4`, ciphertext, accountID, grantGeneration, rebind)
		}
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrSIWCGrantChanged
		}
		return nil
	})
}
