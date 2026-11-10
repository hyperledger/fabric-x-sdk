/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package queryservice

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/state"
)

// Store reads world state from the query service. It keeps no state of its
// own: every read goes to the query service.
//
// Each snapshot is a query-service view, so a transaction sees one consistent
// state. The query service only has the latest state, so SnapshotAt returns
// state.ErrHistoryUnavailable: use Snapshot.
//
// It can still sit where a local database would and receive blocks, but only
// to track the height a synchronizer resumes from.
//
// The query service's defaults are tuned for bulk reads. For this kind of use,
// set min-batch-keys and max-batch-wait low, or every read waits the full batch
// window; keep view-aggregation-window short, or snapshots are stale; and raise
// its rate limit to fit the readers.
type Store struct {
	client Client
	height atomic.Uint64
}

var _ state.KVS = (*Store)(nil)

// NewStore returns a Store reading through client.
func NewStore(client Client) *Store {
	return &Store{client: client}
}

// Snapshot opens a view of the latest state. Reads through the snapshot use
// ctx, so cancelling it, for example when the request a simulation serves is
// abandoned, stops them. Close the snapshot when done; Close ends the view even
// after ctx is cancelled.
func (s *Store) Snapshot(ctx context.Context) (state.Snapshot, error) {
	viewID, err := s.client.BeginView(ctx)
	if err != nil {
		return nil, err
	}
	return &snapshot{ctx: ctx, store: s, viewID: viewID, cache: map[string]*blocks.WriteRecord{}}, nil
}

// SnapshotAt returns state.ErrHistoryUnavailable: the query service has no
// history, and the store cannot tell whether block is still the latest.
func (s *Store) SnapshotAt(context.Context, uint64) (state.Snapshot, error) {
	return nil, state.ErrHistoryUnavailable
}

// Get reads the latest committed record, or nil if there is none.
func (s *Store) Get(namespace, key string) (*blocks.WriteRecord, error) {
	return s.get(context.Background(), "", namespace, key)
}

func (s *Store) get(ctx context.Context, viewID, namespace, key string) (*blocks.WriteRecord, error) {
	rows, err := s.client.GetRows(ctx, viewID, namespace, [][]byte{[]byte(key)})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if string(r.Key) == key {
			return &blocks.WriteRecord{Namespace: namespace, Key: key, Version: r.Version, Value: r.Value}, nil
		}
	}
	return nil, nil
}

// Handle records the block's height and nothing else: the committer has
// already applied its writes. Blocks arrive one at a time.
func (s *Store) Handle(_ context.Context, b blocks.Block) error {
	if b.Number > s.height.Load() {
		s.height.Store(b.Number)
	}
	return nil
}

// SetBlockNumber sets the height to resume from, for example the committer's
// current height, so a synchronizer does not replay blocks this store has no
// use for. Call before the synchronizer starts.
func (s *Store) SetBlockNumber(n uint64) { s.height.Store(n) }

// BlockNumber returns the last block seen.
func (s *Store) BlockNumber(context.Context) (uint64, error) { return s.height.Load(), nil }

// Close closes the client.
func (s *Store) Close() error { return s.client.Close() }

// snapshot reads through one view, caching each key so it is fetched once.
type snapshot struct {
	ctx    context.Context
	store  *Store
	viewID string
	mu     sync.Mutex
	cache  map[string]*blocks.WriteRecord // nil value: known absent
}

// Get reads a record in the snapshot, or nil if there is none.
func (s *snapshot) Get(namespace, key string) (*blocks.WriteRecord, error) {
	ck := namespace + "\x00" + key

	s.mu.Lock()
	rec, ok := s.cache[ck]
	s.mu.Unlock()
	if ok {
		return rec, nil
	}

	rec, err := s.store.get(s.ctx, s.viewID, namespace, key)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[ck] = rec
	s.mu.Unlock()
	return rec, nil
}

// Close ends the view. It keeps ctx's values but not its cancellation, so an
// abandoned request still ends its view rather than leaving it to expire.
func (s *snapshot) Close() error {
	return s.store.client.EndView(context.WithoutCancel(s.ctx), s.viewID)
}
