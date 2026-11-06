/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// Package state provides world-state stores that an endorser can simulate
// transactions against, and a SimulationStore that records the resulting
// read/write set.
//
// Every store implements KVS: it is kept up to date by handing it committed
// blocks, and it opens consistent read views (snapshots) of the latest or an
// earlier height. VersionedDB is a SQLite-backed store that keeps the full
// history. The queryservice subpackage reads from the Fabric-X query service
// instead of keeping state locally.
package state

import (
	"context"
	"errors"

	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// ErrHistoryUnavailable is returned by SnapshotAt for a height the store does
// not retain.
var ErrHistoryUnavailable = errors.New("state: history unavailable at requested height")

// Snapshot is a read-only view of committed state at one height. Blocks
// committed after it was opened do not affect its reads. Close it when done:
// a store may hold resources for it until then.
type Snapshot interface {
	// Get returns the record for key in namespace, or nil if the key was never
	// written. A deleted key may come back as a record with IsDelete set.
	Get(namespace, key string) (*blocks.WriteRecord, error)
	Close() error
}

// Snapshotter opens snapshots.
type Snapshotter interface {
	// Snapshot opens a view of the latest committed state.
	Snapshot(ctx context.Context) (Snapshot, error)
	// SnapshotAt opens a view of the state as of block. A block higher than
	// the store's height gets the latest state.
	// A block older than the store's history returns ErrHistoryUnavailable.
	// Stores that don't retain history always return ErrHistoryUnavailable.
	SnapshotAt(ctx context.Context, block uint64) (Snapshot, error)
}

// KVS is a world-state store, kept up to date by handing it committed blocks.
// Get, from blocks.RecordGetter, reads the latest committed state.
type KVS interface {
	Snapshotter
	blocks.BlockHandler
	blocks.RecordGetter
	BlockNumber(ctx context.Context) (uint64, error)
	Close() error
}
