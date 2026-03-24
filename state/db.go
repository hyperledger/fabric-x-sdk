/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package state provides a versioned insert-only database that can be used to construct
// a local world state. For example, an endorser could use it to perform actions on a
// recent state in order to generate a read/write set based on custom business logic.
package state

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/state/sqlite"
)

// NewReadDB returns a readonly VersionedDB backed by SQLite.
// It allows multiple concurrent connections thanks to WAL mode.
// Note that the tables are not created unless the WriteDB is called
// at least once.
func NewReadDB(channel, connStr string) (*VersionedDB, error) {
	db, err := sqlite.OpenReadOnly(connStr)
	if err != nil {
		return nil, err
	}

	return &VersionedDB{
		channel: channel,
		backend: db,
	}, nil
}

// NewWriteDB returns a SQLite database for read-write access. It enforces a single connection.
// When calling the constructor, the tables are created if they don't exist yet.
func NewWriteDB(channel, connStr string) (*VersionedDB, error) {
	db, err := sqlite.Open(connStr)
	if err != nil {
		return nil, err
	}

	store := &VersionedDB{
		channel: channel,
		backend: db,
	}
	if err := store.init(); err != nil {
		db.Close() //nolint:errcheck
		return nil, err
	}
	return store, nil
}

// VersionedDB provides persistence for read/write sets per channel.
type VersionedDB struct {
	channel string
	backend *sql.DB
}

// Init creates the world state table for a channel if it doesn't exist.
func (db *VersionedDB) init() error {
	schema := `
	CREATE TABLE IF NOT EXISTS worldstate (
		channel TEXT NOT NULL,
		namespace TEXT NOT NULL,
		key TEXT NOT NULL,
		version_block BIGINT NOT NULL,
		version_tx INTEGER NOT NULL,
		version BIGINT NOT NULL,
		value BYTEA,
		is_delete BOOLEAN NOT NULL DEFAULT false,
		tx_id TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (channel, namespace, key, version_block, version_tx)
	);
	CREATE INDEX IF NOT EXISTS idx_worldstate_ns_key ON worldstate (channel, namespace, key);
	CREATE INDEX IF NOT EXISTS idx_worldstate_block_tx ON worldstate (version_block, version_tx);
	CREATE INDEX IF NOT EXISTS idx_worldstate_version ON worldstate (channel, namespace, key, version);

	CREATE TABLE IF NOT EXISTS channel_progress (
		channel TEXT PRIMARY KEY,
		last_block BIGINT NOT NULL
	);
	`
	_, err := db.backend.Exec(schema)
	if err != nil {
		return fmt.Errorf("init tables: %w", err)
	}
	return nil
}

// Handle implements the BlockHandler interface for the BlockProcessor. It wraps UpdateWorldState.
func (db *VersionedDB) Handle(ctx context.Context, b blocks.Block) error {
	return db.UpdateWorldState(ctx, b)
}

// UpdateWorldState inserts all transactions of a block in a single transaction.
// The version for each write is computed as MAX(version)+1 for that (namespace,key),
// so concurrent writes to the same key within a batch receive consecutive versions.
// UpdateWorldState fails if any of the transactions in the block has been processed before.
func (db *VersionedDB) UpdateWorldState(ctx context.Context, b blocks.Block) error {
	sqlTx, err := db.backend.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch insert: %w", err)
	}
	defer sqlTx.Rollback() //nolint:errcheck

	// If we wanted the call to be idempotent, we would add:
	// ON CONFLICT (channel, namespace, key, version_block, version_tx) DO NOTHING
	var stmt *sql.Stmt
	stmt, err = sqlTx.Prepare(`
	INSERT INTO worldstate (channel, namespace, key, version_block, version_tx, version, value, is_delete, tx_id)
	VALUES ($1, $2, $3, $4, $5,
		COALESCE((SELECT MAX(version) + 1 FROM worldstate WHERE channel=$1 AND namespace=$2 AND key=$3), 0),
		$6, $7, $8)
	`)
	if err != nil {
		return fmt.Errorf("prepare batch insert: %w", err)
	}
	defer stmt.Close() //nolint:errcheck

	for _, tx := range b.Transactions {
		if !tx.Valid {
			continue
		}
		for _, nsrws := range tx.NsRWS {
			for _, w := range nsrws.RWS.Writes {
				if _, err := stmt.Exec(db.channel, nsrws.Namespace, w.Key, b.Number, tx.Number, w.Value, w.IsDelete, tx.ID); err != nil {
					return fmt.Errorf("batch insert exec: %w", err)
				}
			}
		}
	}
	query := `
	INSERT INTO channel_progress (channel, last_block)
	VALUES ($1, $2)
	ON CONFLICT (channel) DO UPDATE SET last_block = EXCLUDED.last_block
	WHERE EXCLUDED.last_block > channel_progress.last_block;
	`
	if _, err = sqlTx.Exec(query, db.channel, b.Number); err != nil {
		return fmt.Errorf("update last block: %w", err)
	}

	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("commit batch insert: %w", err)
	}
	return nil
}

// Get returns the state of a key as of a given block height (time-travel).
// It returns the write with the highest version whose block height is ≤ lastBlock.
func (db *VersionedDB) Get(namespace, key string, lastBlock uint64) (*blocks.WriteRecord, error) {
	query := `
	SELECT namespace, key, version_block, version_tx, version, value, is_delete, tx_id
	FROM worldstate
	WHERE channel = $1 AND namespace = $2 AND key = $3 AND version_block <= $4
	ORDER BY version DESC
	LIMIT 1;
	`

	row := db.backend.QueryRow(query, db.channel, namespace, key, lastBlock)
	var w blocks.WriteRecord
	if err := row.Scan(&w.Namespace, &w.Key, &w.BlockNum, &w.TxNum, &w.Version, &w.Value, &w.IsDelete, &w.TxID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get: %w", err)
	}
	return &w, nil
}

// GetCurrent returns the latest version of a key in a namespace.
func (db *VersionedDB) GetCurrent(namespace, key string) (*blocks.WriteRecord, error) {
	query := `
	SELECT namespace, key, version_block, version_tx, version, value, is_delete, tx_id
	FROM worldstate
	WHERE channel = $1 AND namespace = $2 AND key = $3
	ORDER BY version DESC
	LIMIT 1;
	`

	row := db.backend.QueryRow(query, db.channel, namespace, key)
	var w blocks.WriteRecord
	if err := row.Scan(&w.Namespace, &w.Key, &w.BlockNum, &w.TxNum, &w.Version, &w.Value, &w.IsDelete, &w.TxID); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get current: %w", err)
	}
	return &w, nil
}

// GetHistory returns all versions of a key ordered by block height.
func (db *VersionedDB) GetHistory(namespace, key string) ([]blocks.WriteRecord, error) {
	query := `
	SELECT namespace, key, version_block, version_tx, version, value, is_delete, tx_id
	FROM worldstate
	WHERE channel = $1 AND namespace = $2 AND key = $3
	ORDER BY version_block, version_tx;
	`

	rows, err := db.backend.Query(query, db.channel, namespace, key)
	if err != nil {
		return nil, fmt.Errorf("get history: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var result []blocks.WriteRecord
	for rows.Next() {
		var w blocks.WriteRecord
		if err := rows.Scan(&w.Namespace, &w.Key, &w.BlockNum, &w.TxNum, &w.Version, &w.Value, &w.IsDelete, &w.TxID); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		result = append(result, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}
	return result, nil
}

// BlockNumber returns the highest block number stored for the given channel.
// Returns 0 if no blocks have been processed yet. Note: this is ambiguous with block 0
// being the last processed block — callers that need to distinguish these cases should
// start delivery from block 0 when this returns 0, relying on the DB's idempotency to
// handle any re-delivery of already-processed blocks.
func (db *VersionedDB) BlockNumber(ctx context.Context) (uint64, error) {
	var lastBlock sql.NullInt64
	err := db.backend.QueryRowContext(ctx, "SELECT last_block FROM channel_progress WHERE channel = $1", db.channel).Scan(&lastBlock)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		return 0, fmt.Errorf("query last processed block: %w", err)
	}
	if !lastBlock.Valid {
		return 0, nil
	}
	return uint64(lastBlock.Int64), nil
}

func (db *VersionedDB) Close() error {
	return db.backend.Close()
}
