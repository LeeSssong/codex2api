package database

import (
	"context"
	"database/sql"
	"errors"
)

// IPv6 state storage is separate from answer-validated state pool entries.
func (db *DB) InitIPv6State(ctx context.Context) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS codex_ipv6_state_config (id INTEGER PRIMARY KEY, data TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS codex_ipv6_states (account_id BIGINT NOT NULL, model TEXT NOT NULL, data TEXT NOT NULL, secret TEXT NOT NULL, PRIMARY KEY(account_id,model))`,
	} {
		if _, err := db.conn.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func (db *DB) LoadIPv6StateConfig(ctx context.Context) (string, error) {
	var data string
	err := db.conn.QueryRowContext(ctx, `SELECT data FROM codex_ipv6_state_config WHERE id=1`).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return data, err
}

func (db *DB) SaveIPv6StateConfig(ctx context.Context, data string) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		_, err := db.conn.ExecContext(ctx, `INSERT INTO codex_ipv6_state_config(id,data) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, data)
		return err
	})
}

func (db *DB) ListIPv6States(ctx context.Context) ([]StatePoolRow, error) {
	rows, err := db.conn.QueryContext(ctx, `SELECT account_id,model,data,secret FROM codex_ipv6_states`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []StatePoolRow{}
	for rows.Next() {
		var row StatePoolRow
		if err := rows.Scan(&row.AccountID, &row.Model, &row.Data, &row.Secret); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (db *DB) SaveIPv6State(ctx context.Context, row StatePoolRow) error {
	return db.withSQLiteWriteLock(ctx, func() error {
		_, err := db.conn.ExecContext(ctx, `INSERT INTO codex_ipv6_states(account_id,model,data,secret) VALUES($1,$2,$3,$4)
		 ON CONFLICT(account_id,model) DO UPDATE SET data=excluded.data,secret=excluded.secret`, row.AccountID, row.Model, row.Data, row.Secret)
		return err
	})
}
