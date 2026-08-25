/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package state provides a versioned insert-only database that can be used to construct
// a local world state. For example, an endorser could use it to perform actions on a
// recent state in order to generate a read/write set based on custom business logic.
package state

import (
	"bytes"
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
// UpdateWorldState is idempotent: replaying a block whose writes are all already
// present, with identical content, is a no-op. If a write collides with an existing
// row at the same (channel, namespace, key, version_block, version_tx) but with
// different content, that indicates a bug elsewhere (e.g. non-deterministic block
// parsing) rather than a legitimate replay, and UpdateWorldState returns an error.
func (db *VersionedDB) UpdateWorldState(ctx context.Context, b blocks.Block) error {
	sqlTx, err := db.backend.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin batch insert: %w", err)
	}
	defer sqlTx.Rollback() //nolint:errcheck

	var stmt *sql.Stmt
	stmt, err = sqlTx.Prepare(`
	INSERT INTO worldstate (channel, namespace, key, version_block, version_tx, version, value, is_delete, tx_id)
	VALUES ($1, $2, $3, $4, $5,
		COALESCE((SELECT MAX(version) + 1 FROM worldstate WHERE channel=$1 AND namespace=$2 AND key=$3), 0),
		$6, $7, $8)
	ON CONFLICT (channel, namespace, key, version_block, version_tx) DO NOTHING
	`)
	if err != nil {
		return fmt.Errorf("prepare batch insert: %w", err)
	}
	defer stmt.Close() //nolint:errcheck

	checkStmt, err := sqlTx.Prepare(`
	SELECT value, is_delete, tx_id FROM worldstate
	WHERE channel=$1 AND namespace=$2 AND key=$3 AND version_block=$4 AND version_tx=$5
	`)
	if err != nil {
		return fmt.Errorf("prepare conflict check: %w", err)
	}
	defer checkStmt.Close() //nolint:errcheck

	for _, tx := range b.Transactions {
		if !tx.Valid {
			continue
		}
		for _, nsrws := range tx.NsRWS {
			for _, w := range nsrws.RWS.Writes {
				res, err := stmt.Exec(db.channel, nsrws.Namespace, w.Key, b.Number, tx.Number, w.Value, w.IsDelete, tx.ID)
				if err != nil {
					return fmt.Errorf("batch insert exec: %w", err)
				}
				affected, err := res.RowsAffected()
				if err != nil {
					return fmt.Errorf("batch insert rows affected: %w", err)
				}
				if affected > 0 {
					continue
				}
				// The row already existed (primary key conflict). Verify it's identical.
				var existingValue []byte
				var existingIsDelete bool
				var existingTxID string
				row := checkStmt.QueryRow(db.channel, nsrws.Namespace, w.Key, b.Number, tx.Number)
				if err := row.Scan(&existingValue, &existingIsDelete, &existingTxID); err != nil {
					return fmt.Errorf("check conflicting write: %w", err)
				}
				if !bytes.Equal(existingValue, w.Value) || existingIsDelete != w.IsDelete || existingTxID != tx.ID {
					return fmt.Errorf("conflicting write at channel=%s namespace=%s key=%s block=%d tx=%d: "+
						"existing (is_delete=%v tx_id=%s) differs from replayed (is_delete=%v tx_id=%s)",
						db.channel, nsrws.Namespace, w.Key, b.Number, tx.Number,
						existingIsDelete, existingTxID, w.IsDelete, tx.ID)
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
