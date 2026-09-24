/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package blocks has the tools to parse Fabric or Fabric-X blocks.
// It also includes a simple Processor that can execute pluggable
// handlers.
package blocks

// ReadWriteSet is the outcome of an optimistically executed transaction.
type ReadWriteSet struct {
	Reads  []KVRead
	Writes []KVWrite
}

// KVRead is a read observed during transaction simulation. Version is nil when the key was absent.
type KVRead struct {
	Key     string
	Version *Version
}

// KVWrite is a write or delete recorded during transaction simulation.
type KVWrite struct {
	Key      string
	IsDelete bool
	Value    []byte
}

// Version identifies the state of a key at the time it was read. The fields used
// for MVCC conflict detection depend on the ledger type: Fabric uses (BlockNum, TxNum)
// while Fabric-X uses only BlockNum (which maps to the per-key monotonic version).
type Version struct {
	BlockNum uint64
	TxNum    uint64
}

// WriteRecord represents a single write or delete in the world state.
type WriteRecord struct {
	Namespace string
	Key       string
	BlockNum  uint64 // block height; used for time-travel queries and Fabric MVCC
	TxNum     uint64 // tx index within block; used for Fabric MVCC (0 for Fabric-X)
	Version   uint64 // monotonically increasing per (namespace, key); used for Fabric-X MVCC
	Value     []byte
	IsDelete  bool
	TxID      string
}

// NsReadWriteSet groups the reads and writes that belong to a single namespace.
type NsReadWriteSet struct {
	Namespace string
	RWS       ReadWriteSet
}

// Block is the parsed representation of a Fabric or Fabric-X block.
type Block struct {
	Number     uint64
	Hash       []byte
	ParentHash []byte
	Timestamp  int64
	// Transactions in a block. Invalid transactions are included. Index
	// in the Transactions slice is not guaranteed to be the same as in
	// the actual block: use the tx.Number field for that.
	Transactions []Transaction
}

// Transaction is a parsed endorser transaction from a block. Both valid and invalid
// transactions are included; check Valid() before processing writes.
type Transaction struct {
	ID        string
	Number    int64
	InputArgs [][]byte
	Status    Status
	RawCode   int32
	Reason    string
	// Event is the payload of the chaincode event emitted by the transaction, if any.
	// A transaction emits at most one event.
	Event     []byte
	EventName string
	Payload   []byte
	NsRWS     []NsReadWriteSet
}

// Valid reports whether the transaction committed successfully.
func (t Transaction) Valid() bool {
	return t.Status.Valid()
}

// SetStatus records status and its underlying ledger-specific raw code and
// human-readable reason.
func (t *Transaction) SetStatus(status Status, rawCode int32, reason string) {
	t.Status = status
	t.RawCode = rawCode
	t.Reason = reason
}
