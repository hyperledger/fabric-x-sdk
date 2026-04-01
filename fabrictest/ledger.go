/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package fabrictest mimics a minimal in-memory Fabric or Fabric-X network for tests.
// It implements the orderer Broadcast API and the peer Deliver endpoint.
package fabrictest

import (
	"context"
	"sync"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"google.golang.org/protobuf/proto"

	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/state"
)

// ledger represents the blockchain and world state.
// Everything is kept in memory. In this mock setup, the orderer appends validated
// blocks and write records. The peer subscribes to receive all new blocks and pushes
// them through its Deliver endpoint.
type ledger struct {
	mu        sync.Mutex
	blocks    []*common.Block
	subs      []chan *common.Block
	db        *state.VersionedDB
	parser    blocks.BlockParser
	validator *blocks.MVCCValidator
}

// newLedger creates a new ledger.
func newLedger(db *state.VersionedDB, parser blocks.BlockParser, validator *blocks.MVCCValidator) *ledger {
	return &ledger{
		db:        db,
		blocks:    make([]*common.Block, 0),
		parser:    parser,
		validator: validator,
	}
}

// process validates the batch of transactions and stores them in a block.
// Transactions are validated in order; a valid transaction's writes are recorded
// as pending so that later transactions in the same block can detect intra-block
// MVCC conflicts (first writer wins, matching real Fabric semantics).
func (l *ledger) process(ctx context.Context, env []*common.Envelope) error {
	// the number for the current block (process is only called synchronously).
	blockNum := l.height()

	// create block in fabric format just so we can parse it again. The status code in txFilter will be updated later.
	fbl := makeBlock(blockNum, env)

	// get the read/write set and transaction id. We don't care about the other fields.
	bl, err := l.parser.Parse(fbl)
	if err != nil {
		return err // should not happen
	}

	// Set transaction numbers (position in block) before validation.
	for i := range bl.Transactions {
		bl.Transactions[i].Number = int64(i)
	}

	// Validate all transactions; updates bl.Transactions[i].Valid and returns txFilter.
	txFilter, err := l.validator.Validate(&bl)
	if err != nil {
		return err
	}
	copy(fbl.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER], txFilter)

	// commit them to the ledger
	return l.commit(ctx, bl, fbl)
}

// makeBlock creates a minimal block.
func makeBlock(blockNum uint64, envs []*common.Envelope) *common.Block {
	data := make([][]byte, len(envs))
	for i, env := range envs {
		b, _ := proto.Marshal(env)
		data[i] = b
	}

	return &common.Block{
		// Header: we skip the hashes.
		Header: &common.BlockHeader{Number: blockNum},
		// Data contains the original transactions.
		Data: &common.BlockData{Data: data},
		// Metadata: signatures, last_config [deprecated], transactions_filter.
		// Pre-allocate the txFilter so we can copy in the actual statuses later.
		Metadata: &common.BlockMetadata{Metadata: [][]byte{{}, {}, make([]byte, len(envs))}},
	}
}

// commit adds a block and applies the writes.
func (l *ledger) commit(ctx context.Context, bl blocks.Block, fbl *common.Block) error {
	if err := l.db.UpdateWorldState(ctx, bl); err != nil {
		return err
	}

	l.mu.Lock()
	l.blocks = append(l.blocks, fbl)
	l.mu.Unlock()

	for _, ch := range l.subs {
		select {
		case ch <- fbl:
		default:
		}
	}
	return nil
}

// subscribe is used by the peer to get blocks. It returns a snapshot of all
// blocks committed so far and a channel for new blocks. The snapshot and channel
// registration are done atomically under the lock so no committed blocks are lost.
func (l *ledger) subscribe() ([]*common.Block, chan *common.Block) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ch := make(chan *common.Block, 10)
	l.subs = append(l.subs, ch)

	existing := make([]*common.Block, len(l.blocks))
	copy(existing, l.blocks)

	return existing, ch
}

// height is the block height
func (l *ledger) height() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return uint64(len(l.blocks) + 1)
}

// close shuts down the ledger by closing all subscriber channels (which causes
// goroutines blocked in Deliver to exit) and closing the world-state DB.
func (l *ledger) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ch := range l.subs {
		close(ch)
	}
	l.subs = nil
	l.db.Close()
}
