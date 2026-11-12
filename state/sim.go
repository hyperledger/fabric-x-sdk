/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package state

import (
	"errors"

	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// SimulationStore implements a very basic set of state interactions on a snapshot of the world state.
// It records reads and writes. By default, like in Fabric, you cannot 'read your own writes' but this can be configured.
// Function signatures correspond to the 'chaincode stub' where possible.
type SimulationStore struct {
	namespace         string
	snap              Snapshot
	blockNum          uint64
	reads             map[string]blocks.KVRead
	writes            map[string]blocks.KVWrite
	monotonicVersions bool // if true, KVRead.Version is built from WriteRecord.Version (fabric-x MVCC semantics)
}

// NewSimulationStore returns a SimulationStore that reads from snap, a view of
// the state at blockNum, and records reads and writes. The caller owns snap and
// closes it when the simulation is done.
// monotonicVersions controls MVCC semantics: when true, KVRead versions use the per-key
// monotonic version counter (Fabric-X); when false, they use (blockNum, txNum) (standard Fabric).
func NewSimulationStore(snap Snapshot, namespace string, blockNum uint64, monotonicVersions bool) *SimulationStore {
	return &SimulationStore{
		namespace:         namespace,
		snap:              snap,
		blockNum:          blockNum,
		monotonicVersions: monotonicVersions,
		reads:             make(map[string]blocks.KVRead),
		writes:            make(map[string]blocks.KVWrite),
	}
}

// GetState behaves similar to in Fabric, with the exception that we _can_ read
// our own writes if explicitly configured.
// read own write (if enabled) -> return last written value, nil
// read own delete (if enabled) -> return nil, nil
// no result -> store read with nil version, return nil, nil
// deleted result (Fabric) -> no read, return nil, nil
// deleted result (Fabric-X) -> store read version, return nil, nil
// result -> store read version, return value, nil
func (s *SimulationStore) GetState(key string) ([]byte, error) {
	// return early, we don't record reading your own writes
	if record, ok := s.writes[key]; ok {
		if record.IsDelete {
			return nil, nil
		}
		return record.Value, nil
	}

	// get from store snapshot
	record, err := s.snap.Get(s.namespace, key)
	if err != nil {
		return nil, err
	}

	var val []byte
	var read = blocks.KVRead{Key: key}
	if record != nil {
		// Fabric doesn't add a read marker if the value is deleted. Fabric-X
		// keeps a deleted key's row and version, so a read of it must carry
		// that version. Fabric-X blocks never carry IsDelete (see
		// fabricx.DecodeNamespaces), so the Fabric-X case is defensive.
		if record.IsDelete && !s.monotonicVersions {
			return nil, nil
		}
		if s.monotonicVersions {
			read.Version = &blocks.Version{BlockNum: record.Version}
		} else {
			read.Version = &blocks.Version{
				BlockNum: record.BlockNum,
				TxNum:    record.TxNum,
			}
		}

		if !record.IsDelete {
			val = record.Value
		}
	}
	s.reads[key] = read

	return val, nil
}

// PutState puts the specified `key` and `value` into the transaction's
// writeset as a data-write proposal. PutState doesn't effect the ledger
// until the transaction is validated and successfully committed.
// Simple keys must not be an empty string and must not start with a
// null character (0x00) in order to avoid range query collisions with
// composite keys, which internally get prefixed with 0x00 as composite
// key namespace. In addition, if using CouchDB, keys can only contain
// valid UTF-8 strings and cannot begin with an underscore ("_").
func (s *SimulationStore) PutState(key string, value []byte) error {
	if len(key) == 0 {
		return errors.New("key is empty")
	}
	if len(value) == 0 {
		return s.DelState(key)
	}
	s.writes[key] = blocks.KVWrite{Key: key, Value: value}
	return nil
}

// DelState records the specified `key` to be deleted in the writeset of
// the transaction proposal. The `key` and its value will be deleted from
// the ledger when the transaction is validated and successfully committed.
func (s *SimulationStore) DelState(key string) error {
	s.writes[key] = blocks.KVWrite{Key: key, IsDelete: true}
	return nil
}

// Result returns the accumulated read-write set, ready to be passed to endorsement.Success.
func (s *SimulationStore) Result() blocks.ReadWriteSet {
	rws := blocks.ReadWriteSet{
		Reads:  make([]blocks.KVRead, 0, len(s.reads)),
		Writes: make([]blocks.KVWrite, 0, len(s.writes)),
	}
	for _, r := range s.reads {
		rws.Reads = append(rws.Reads, r)
	}
	for _, w := range s.writes {
		rws.Writes = append(rws.Writes, w)
	}
	return rws
}

// Version is the blockheight of this snapshot.
func (s *SimulationStore) Version() uint64 {
	return s.blockNum
}
