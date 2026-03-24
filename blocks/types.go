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

type KVRead struct {
	Key     string
	Version *Version
}

type KVWrite struct {
	Key      string
	IsDelete bool
	Value    []byte
}

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

type NsReadWriteSet struct {
	Namespace string
	RWS       ReadWriteSet
}

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

type Transaction struct {
	ID        string
	Number    int64
	InputArgs [][]byte
	Status    int
	Valid     bool
	Events    []byte
	NsRWS     []NsReadWriteSet
}
