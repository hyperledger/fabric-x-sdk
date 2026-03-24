/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package local

import (
	"context"
	"fmt"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/state"
	_ "modernc.org/sqlite"
)

// --- mocks ---

type fixedPackager struct{ env *common.Envelope }

func (f *fixedPackager) PackageTx(_ sdk.Endorsement) (*common.Envelope, error) {
	return f.env, nil
}

type fixedParser struct {
	rws  blocks.ReadWriteSet
	txID string
}

func (f *fixedParser) ParseTx(_ *common.Envelope) (*blocks.Transaction, error) {
	return &blocks.Transaction{
		ID:    f.txID,
		Valid: true,
		NsRWS: []blocks.NsReadWriteSet{
			{Namespace: "ns", RWS: f.rws},
		},
	}, nil
}

// --- test helpers ---

func newTestDB(t *testing.T) *state.VersionedDB {
	t.Helper()
	connStr := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := state.NewWriteDB("chan1", connStr)
	if err != nil {
		t.Fatalf("NewSqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() }) //nolint:errcheck
	return db
}

func newSubmitter(t *testing.T, db *state.VersionedDB, rws blocks.ReadWriteSet) *LocalSubmitter {
	t.Helper()
	return NewLocalSubmitter(db, "chan1", "ns",
		&fixedPackager{env: &common.Envelope{}},
		&fixedParser{rws: rws, txID: "txid"},
		false,
	)
}

// --- tests ---

func TestLocalSubmitter_Submit_BlindWrite(t *testing.T) {
	db := newTestDB(t)
	rws := blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v")}},
	}
	s := newSubmitter(t, db, rws)

	if err := s.Submit(context.Background(), sdk.Endorsement{}); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetCurrent("ns", "k")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || string(got.Value) != "v" {
		t.Errorf("unexpected record: %+v", got)
	}
}

func TestLocalSubmitter_Submit_NoWrites(t *testing.T) {
	db := newTestDB(t)
	rws := blocks.ReadWriteSet{
		Reads: []blocks.KVRead{{Key: "r", Version: nil}},
	}
	s := newSubmitter(t, db, rws)

	if err := s.Submit(context.Background(), sdk.Endorsement{}); err != nil {
		t.Fatal(err)
	}
	// A block is always created, even with no writes.
	n, _ := db.BlockNumber(context.Background())
	if n != 1 {
		t.Errorf("expected block 1, got %d", n)
	}
}

func TestLocalSubmitter_Submit_ReadVersionMatch(t *testing.T) {
	db := newTestDB(t)
	// Pre-populate a record at block 1.
	_ = db.UpdateWorldState(context.Background(), blocks.Block{
		Number: 1,
		Transactions: []blocks.Transaction{{ID: "prev", Number: 0, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
			Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "k", Value: []byte("old")}}},
		}}}},
	})

	rws := blocks.ReadWriteSet{
		Reads:  []blocks.KVRead{{Key: "k", Version: &blocks.Version{BlockNum: 1, TxNum: 0}}},
		Writes: []blocks.KVWrite{{Key: "k", Value: []byte("new")}},
	}
	s := newSubmitter(t, db, rws)

	if err := s.Submit(context.Background(), sdk.Endorsement{}); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestLocalSubmitter_Submit_ReadVersionMismatch(t *testing.T) {
	db := newTestDB(t)
	_ = db.UpdateWorldState(context.Background(), blocks.Block{
		Number: 5,
		Transactions: []blocks.Transaction{{ID: "prev", Number: 0, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
			Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v")}}},
		}}}},
	})

	rws := blocks.ReadWriteSet{
		Reads: []blocks.KVRead{{Key: "k", Version: &blocks.Version{BlockNum: 1, TxNum: 0}}},
	}
	s := newSubmitter(t, db, rws)

	if err := s.Submit(context.Background(), sdk.Endorsement{}); err == nil {
		t.Error("expected read conflict error")
	}
}

func TestLocalSubmitter_Submit_ReadNilButStateExists(t *testing.T) {
	db := newTestDB(t)
	_ = db.UpdateWorldState(context.Background(), blocks.Block{
		Number: 1,
		Transactions: []blocks.Transaction{{ID: "prev", Number: 0, Valid: true, NsRWS: []blocks.NsReadWriteSet{{
			Namespace: "ns", RWS: blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v")}}},
		}}}},
	})

	rws := blocks.ReadWriteSet{
		Reads: []blocks.KVRead{{Key: "k", Version: nil}},
	}
	s := newSubmitter(t, db, rws)

	if err := s.Submit(context.Background(), sdk.Endorsement{}); err == nil {
		t.Error("expected error: read version nil but state exists")
	}
}
