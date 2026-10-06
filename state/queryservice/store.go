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
)

// Store reads world state from the query service. It keeps no state of its
// own: every read goes to the query service.
//
// Each snapshot is a query-service view, so a transaction sees one consistent
// state. The query service only has the latest state, so there are no
// snapshots at past blocks.
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

var (
	_ blocks.BlockHandler = (*Store)(nil)
	_ blocks.RecordGetter = (*Store)(nil)
)

// NewStore returns a Store reading through client.
func NewStore(client Client) *Store {
	return &Store{client: client}
}

// NewSnapshot opens a view of the latest state. Close the snapshot when done.
func (s *Store) NewSnapshot() (*Snapshot, error) {
	viewID, err := s.client.BeginView(context.Background())
	if err != nil {
		return nil, err
	}
	return &Snapshot{store: s, viewID: viewID, cache: map[string]*blocks.WriteRecord{}}, nil
}

// Get reads the latest committed record, or nil if there is none.
func (s *Store) Get(namespace, key string) (*blocks.WriteRecord, error) {
	return s.get("", namespace, key)
}

func (s *Store) get(viewID, namespace, key string) (*blocks.WriteRecord, error) {
	rows, err := s.client.GetRows(context.Background(), viewID, namespace, [][]byte{[]byte(key)})
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

// Snapshot reads through one view, caching each key so it is fetched once.
type Snapshot struct {
	store  *Store
	viewID string
	mu     sync.Mutex
	cache  map[string]*blocks.WriteRecord // nil value: known absent
}

// Get reads a record in the snapshot, or nil if there is none.
func (s *Snapshot) Get(namespace, key string) (*blocks.WriteRecord, error) {
	ck := namespace + "\x00" + key

	s.mu.Lock()
	rec, ok := s.cache[ck]
	s.mu.Unlock()
	if ok {
		return rec, nil
	}

	rec, err := s.store.get(s.viewID, namespace, key)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[ck] = rec
	s.mu.Unlock()
	return rec, nil
}

// Close ends the view.
func (s *Snapshot) Close() error {
	return s.store.client.EndView(context.Background(), s.viewID)
}
