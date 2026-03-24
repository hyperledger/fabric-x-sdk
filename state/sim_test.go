/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package state

import (
	"context"
	"errors"
	"testing"

	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// mockReadStore implements ReadStore for testing.
type mockReadStore struct {
	data map[string]*blocks.WriteRecord
	err  error
}

func (m *mockReadStore) Get(namespace, key string, _ uint64) (*blocks.WriteRecord, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.data[namespace+"/"+key], nil
}

func (m *mockReadStore) BlockNumber(_ context.Context) (uint64, error) { return 0, nil }

func newSim(store ReadStore) *SimulationStore {
	return &SimulationStore{
		namespace: "ns",
		store:     store,
		blockNum:  10,
		reads:     make(map[string]blocks.KVRead),
		writes:    make(map[string]blocks.KVWrite),
	}
}

func newSimMonotonic(store ReadStore) *SimulationStore {
	s := newSim(store)
	s.monotonicVersions = true
	return s
}

func TestGetState(t *testing.T) {
	tests := []struct {
		name        string
		storeData   map[string]*blocks.WriteRecord
		storeErr    error
		key         string
		wantValue   []byte
		wantErrMsg  string
		wantReadKey bool // whether a read entry should be recorded
	}{
		{
			name:        "key not found",
			storeData:   map[string]*blocks.WriteRecord{},
			key:         "a",
			wantValue:   nil,
			wantReadKey: true,
		},
		{
			name: "key found",
			storeData: map[string]*blocks.WriteRecord{
				"ns/a": {BlockNum: 5, TxNum: 1, Value: []byte("v")},
			},
			key:         "a",
			wantValue:   []byte("v"),
			wantReadKey: true,
		},
		{
			name: "deleted record returns nil, no read recorded",
			storeData: map[string]*blocks.WriteRecord{
				"ns/a": {BlockNum: 3, TxNum: 0, IsDelete: true},
			},
			key:         "a",
			wantValue:   nil,
			wantReadKey: false,
		},
		{
			name:       "store error propagated",
			storeErr:   errors.New("db down"),
			key:        "a",
			wantErrMsg: "db down",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockReadStore{data: tt.storeData, err: tt.storeErr}
			sim := newSim(store)

			val, err := sim.GetState(tt.key)

			if tt.wantErrMsg != "" {
				if err == nil || err.Error() != tt.wantErrMsg {
					t.Fatalf("expected error %q, got %v", tt.wantErrMsg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(val) != string(tt.wantValue) {
				t.Errorf("value: got %q, want %q", val, tt.wantValue)
			}
			_, recorded := sim.reads[tt.key]
			if recorded != tt.wantReadKey {
				t.Errorf("read recorded=%v, want %v", recorded, tt.wantReadKey)
			}
		})
	}
}

func TestGetState_ReadOwnWrite(t *testing.T) {
	sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
	_ = sim.PutState("a", []byte("written"))

	val, err := sim.GetState("a")
	if err != nil {
		t.Fatal(err)
	}
	if string(val) != "written" {
		t.Errorf("expected written value, got %q", val)
	}
	// The read is NOT recorded because it's a read-own-write.
	if _, ok := sim.reads["a"]; ok {
		t.Error("read-own-write should not record a read entry")
	}
}

func TestGetState_ReadOwnDelete(t *testing.T) {
	sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
	_ = sim.DelState("a")

	val, err := sim.GetState("a")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Errorf("expected nil for deleted key, got %q", val)
	}
}

func TestPutState(t *testing.T) {
	t.Run("basic put", func(t *testing.T) {
		sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
		if err := sim.PutState("k", []byte("v")); err != nil {
			t.Fatal(err)
		}
		w, ok := sim.writes["k"]
		if !ok || w.IsDelete || string(w.Value) != "v" {
			t.Errorf("unexpected write: %+v", w)
		}
	})

	t.Run("empty value becomes delete", func(t *testing.T) {
		sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
		if err := sim.PutState("k", []byte{}); err != nil {
			t.Fatal(err)
		}
		w, ok := sim.writes["k"]
		if !ok || !w.IsDelete {
			t.Errorf("expected delete, got %+v", w)
		}
	})

	t.Run("empty key returns error", func(t *testing.T) {
		sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
		if err := sim.PutState("", []byte("v")); err == nil {
			t.Error("expected error for empty key")
		}
	})
}

func TestDelState(t *testing.T) {
	sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
	if err := sim.DelState("k"); err != nil {
		t.Fatal(err)
	}
	w, ok := sim.writes["k"]
	if !ok || !w.IsDelete {
		t.Errorf("expected delete write, got %+v", w)
	}
}

func TestResult(t *testing.T) {
	sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{
		"ns/existing": {BlockNum: 1, TxNum: 0, Value: []byte("x")},
	}})

	_, _ = sim.GetState("existing")
	_ = sim.PutState("new", []byte("y"))

	rws := sim.Result()

	if len(rws.Reads) != 1 || rws.Reads[0].Key != "existing" {
		t.Errorf("unexpected reads: %+v", rws.Reads)
	}
	if len(rws.Writes) != 1 || rws.Writes[0].Key != "new" {
		t.Errorf("unexpected writes: %+v", rws.Writes)
	}
}

func TestAddLog(t *testing.T) {
	sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
	sim.AddLog([]byte("addr"), [][]byte{[]byte("topic")}, []byte("data"))

	logs := sim.Logs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if string(logs[0].Address) != "addr" {
		t.Errorf("unexpected address: %q", logs[0].Address)
	}
}

func TestVersion(t *testing.T) {
	sim := newSim(&mockReadStore{data: map[string]*blocks.WriteRecord{}})
	if sim.Version() != 10 {
		t.Errorf("expected blockNum 10, got %d", sim.Version())
	}
}

func TestGetState_MonotonicVersion(t *testing.T) {
	// With monotonicVersions=true the read's version must use WriteRecord.Version,
	// not the block/tx numbers. This is what Fabric-X committer expects.
	store := &mockReadStore{data: map[string]*blocks.WriteRecord{
		"ns/a": {BlockNum: 5, TxNum: 1, Version: 3, Value: []byte("v")},
	}}
	sim := newSimMonotonic(store)
	if _, err := sim.GetState("a"); err != nil {
		t.Fatal(err)
	}
	read, ok := sim.reads["a"]
	if !ok {
		t.Fatal("read not recorded")
	}
	if read.Version == nil || read.Version.BlockNum != 3 {
		t.Errorf("expected Version.BlockNum=3, got %+v", read.Version)
	}
}

func TestGetState_BlockTxVersion(t *testing.T) {
	// With monotonicVersions=false the read's version must use BlockNum/TxNum.
	// This is what the standard Fabric committer expects.
	store := &mockReadStore{data: map[string]*blocks.WriteRecord{
		"ns/a": {BlockNum: 5, TxNum: 1, Version: 3, Value: []byte("v")},
	}}
	sim := newSim(store) // monotonicVersions=false
	if _, err := sim.GetState("a"); err != nil {
		t.Fatal(err)
	}
	read, ok := sim.reads["a"]
	if !ok {
		t.Fatal("read not recorded")
	}
	if read.Version == nil || read.Version.BlockNum != 5 || read.Version.TxNum != 1 {
		t.Errorf("expected Version={BlockNum:5, TxNum:1}, got %+v", read.Version)
	}
}

func TestGetState_NotFoundVersion(t *testing.T) {
	// A missing key must produce a read with nil version regardless of mode.
	for _, monotonic := range []bool{false, true} {
		store := &mockReadStore{data: map[string]*blocks.WriteRecord{}}
		var sim *SimulationStore
		if monotonic {
			sim = newSimMonotonic(store)
		} else {
			sim = newSim(store)
		}
		if _, err := sim.GetState("missing"); err != nil {
			t.Fatal(err)
		}
		read, ok := sim.reads["missing"]
		if !ok {
			t.Fatal("read not recorded for missing key")
		}
		if read.Version != nil {
			t.Errorf("monotonic=%v: expected nil version for missing key, got %+v", monotonic, read.Version)
		}
	}
}
