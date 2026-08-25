/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package state

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/hyperledger/fabric-x-sdk/blocks"
	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *VersionedDB {
	t.Helper()
	// Each test gets its own in-memory DB via a unique file name.
	connStr := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := NewWriteDB("chan1", connStr)
	if err != nil {
		t.Fatalf("NewSqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() }) //nolint:errcheck
	return db
}

// mustWrite applies a single-key write to the DB, for test setup.
func mustWrite(t *testing.T, db *VersionedDB, ns, key string, block, tx uint64, value []byte) {
	t.Helper()
	if err := db.UpdateWorldState(context.Background(), blocks.Block{
		Number: block,
		Transactions: []blocks.Transaction{{
			ID:     "txid",
			Number: int64(tx),
			Valid:  true,
			NsRWS: []blocks.NsReadWriteSet{{
				Namespace: ns,
				RWS: blocks.ReadWriteSet{
					Writes: []blocks.KVWrite{{Key: key, Value: value}},
				},
			}},
		}},
	}); err != nil {
		t.Fatalf("UpdateWorldState: %v", err)
	}
}

func mustUpdate(t *testing.T, db *VersionedDB, b blocks.Block) {
	t.Helper()
	if err := db.UpdateWorldState(context.Background(), b); err != nil {
		t.Fatalf("UpdateWorldState: %v", err)
	}
}

func TestInit(t *testing.T) {
	newTestDB(t) // schema creation must not error
}

func TestGet_NotFound(t *testing.T) {
	db := newTestDB(t)
	got, err := db.Get("ns", "missing", 99)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestGet_VersionBound(t *testing.T) {
	db := newTestDB(t)
	mustWrite(t, db, "ns", "k", 5, 0, []byte("v5"))
	mustWrite(t, db, "ns", "k", 10, 0, []byte("v10"))

	got, _ := db.Get("ns", "k", 7)
	if got == nil || string(got.Value) != "v5" {
		t.Errorf("expected v5, got %+v", got)
	}
	got, _ = db.Get("ns", "k", 10)
	if got == nil || string(got.Value) != "v10" {
		t.Errorf("expected v10, got %+v", got)
	}
}

func TestGetCurrent(t *testing.T) {
	db := newTestDB(t)
	mustWrite(t, db, "ns", "k", 1, 0, []byte("old"))
	mustWrite(t, db, "ns", "k", 5, 0, []byte("new"))

	got, err := db.GetCurrent("ns", "k")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || string(got.Value) != "new" {
		t.Errorf("expected new, got %+v", got)
	}
}

func TestGetHistory(t *testing.T) {
	db := newTestDB(t)
	mustWrite(t, db, "ns", "k", 3, 0, []byte("v3"))
	mustWrite(t, db, "ns", "k", 1, 0, []byte("v1"))

	history, err := db.GetHistory("ns", "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 records, got %d", len(history))
	}
	if history[0].BlockNum != 1 || history[1].BlockNum != 3 {
		t.Errorf("wrong order: %v", history)
	}
}

func TestBlockNumber_Zero(t *testing.T) {
	db := newTestDB(t)
	n, err := db.BlockNumber(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("expected 0, got %d", n)
	}
}

func TestUpdateWorldState_ReplayIsNoop(t *testing.T) {
	db := newTestDB(t)
	bl := blocks.Block{Number: 2, Transactions: []blocks.Transaction{{
		ID: "txid", Number: 0, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
			Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "a", Value: []byte("va")}}},
		}},
	}}}
	if err := db.UpdateWorldState(t.Context(), bl); err != nil {
		t.Fatal(err)
	}
	before, err := db.GetCurrent("ns", "a")
	if err != nil {
		t.Fatal(err)
	}

	// Replaying the same block must be a no-op: no error, no state change.
	if err := db.UpdateWorldState(t.Context(), bl); err != nil {
		t.Fatalf("expected replay to succeed, got: %v", err)
	}

	after, err := db.GetCurrent("ns", "a")
	if err != nil {
		t.Fatal(err)
	}
	if before.Version != after.Version || !bytes.Equal(before.Value, after.Value) {
		t.Errorf("expected state unchanged by replay, before=%+v after=%+v", before, after)
	}

	history, err := db.GetHistory("ns", "a")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Errorf("expected replay to not create a duplicate row, got %d records", len(history))
	}

	n, err := db.BlockNumber(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected block progress to remain 2, got %d", n)
	}
}

func TestUpdateWorldState_GenuineConflictErrors(t *testing.T) {
	db := newTestDB(t)
	mustWrite(t, db, "ns", "a", 2, 0, []byte("va"))

	// Same (channel, namespace, key, version_block, version_tx) coordinates as the
	// write above, but a different value: this is not a legitimate replay and must
	// be rejected rather than silently discarded.
	conflicting := blocks.Block{Number: 2, Transactions: []blocks.Transaction{
		{ID: "txid", Number: 0, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
			Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "a", Value: []byte("different")}}},
		}}},
		{ID: "tx-other", Number: 1, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
			Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "b", Value: []byte("vb")}}},
		}}},
	}}
	if err := db.UpdateWorldState(t.Context(), conflicting); err == nil {
		t.Fatal("expected error on conflicting write, got nil")
	}

	// The whole call must roll back: "a" keeps its original value, and "b" (from the
	// same failed batch) must not have been inserted either.
	a, err := db.GetCurrent("ns", "a")
	if err != nil {
		t.Fatal(err)
	}
	if a == nil || string(a.Value) != "va" {
		t.Errorf("expected ns/a to keep original value, got %+v", a)
	}
	b, err := db.GetCurrent("ns", "b")
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		t.Errorf("expected ns/b to not be inserted after rollback, got %+v", b)
	}
}

func TestVersion_StartsAtZero(t *testing.T) {
	db := newTestDB(t)
	mustWrite(t, db, "ns", "k", 1, 0, []byte("v"))

	got, err := db.GetCurrent("ns", "k")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Version != 0 {
		t.Errorf("expected version 0 for first write, got %+v", got)
	}
}

func TestVersion_Increments(t *testing.T) {
	db := newTestDB(t)
	mustWrite(t, db, "ns", "k", 1, 0, []byte("v1"))
	mustWrite(t, db, "ns", "k", 2, 0, []byte("v2"))
	mustWrite(t, db, "ns", "k", 3, 0, []byte("v3"))

	history, err := db.GetHistory("ns", "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("expected 3 records, got %d", len(history))
	}
	for i, h := range history {
		if h.Version != uint64(i) {
			t.Errorf("record %d: expected version %d, got %d", i, i, h.Version)
		}
	}
}

func TestVersion_IndependentKeys(t *testing.T) {
	db := newTestDB(t)
	// Two writes in the same block, different txs.
	mustUpdate(t, db, blocks.Block{
		Number: 1,
		Transactions: []blocks.Transaction{
			{ID: "txid", Number: 0, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
				Namespace: "ns",
				RWS:       blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "a", Value: []byte("va")}}},
			}}},
			{ID: "txid", Number: 1, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
				Namespace: "ns",
				RWS:       blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "b", Value: []byte("vb")}}},
			}}},
		},
	})

	a, _ := db.GetCurrent("ns", "a")
	b, _ := db.GetCurrent("ns", "b")
	if a == nil || b == nil {
		t.Fatal("expected both keys to exist")
	}
	if a.Version != 0 || b.Version != 0 {
		t.Errorf("independent keys should both start at 0, got a=%d b=%d", a.Version, b.Version)
	}
}

func TestUpdateWorldState_BlockProgress(t *testing.T) {
	db := newTestDB(t)
	mustUpdate(t, db, blocks.Block{
		Number: 2,
		Transactions: []blocks.Transaction{
			{ID: "tx1", Number: 0, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
				Namespace: "ns",
				RWS:       blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "a", Value: []byte("va")}}},
			}}},
			{ID: "tx2", Number: 1, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
				Namespace: "ns",
				RWS:       blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "b", Value: []byte("vb")}}},
			}}},
		},
	})
	n, err := db.BlockNumber(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("expected block 2, got %d", n)
	}
}
