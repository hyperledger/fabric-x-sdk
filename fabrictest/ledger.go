/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package fabrictest mimics a minimal in-memory Fabric or Fabric-X network for tests.
// It implements the orderer Broadcast API and the peer Deliver endpoint.
package fabrictest

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/protoutil"
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
	db        *state.VersionedDB // own world state; nil when an external one (db1) is used
	parser    blocks.BlockParser
	validator *blocks.MVCCValidator
}

// newLedger creates a new ledger, seeded with the genesis block so the chain
// starts at block 0 exactly as a real Fabric or Fabric-X channel does.
func newLedger(db *state.VersionedDB, parser blocks.BlockParser, validator *blocks.MVCCValidator) *ledger {
	return &ledger{
		db:        db,
		blocks:    []*common.Block{makeGenesisBlock()},
		parser:    parser,
		validator: validator,
	}
}

// makeGenesisBlock creates block 0: a config block holding a single CONFIG envelope,
// mirroring the shape Fabric and Fabric-X networks start from. A real network stores
// the channel configuration in it; fabrictest has no channel config, so the payload is
// otherwise empty and the block only has to be recognizable. That is enough for
// protoutil.IsConfigBlock and anything else that classifies a block by inspecting
// Data.Data[0] to behave as it would against a real network.
//
// Genesis chains onto nothing, so its PreviousHash is nil — as in Fabric, where a
// client walking the chain stops there rather than finding a hash of nothing.
func makeGenesisBlock() *common.Block {
	env := &common.Envelope{
		Payload: protoutil.MarshalOrPanic(&common.Payload{
			Header: &common.Header{
				ChannelHeader: protoutil.MarshalOrPanic(&common.ChannelHeader{
					Type: int32(common.HeaderType_CONFIG),
				}),
			},
		}),
	}
	return makeBlock(0, nil, []*common.Envelope{env})
}

// process validates the batch of transactions and stores them in a block.
// Transactions are validated in order; a valid transaction's writes are recorded
// as pending so that later transactions in the same block can detect intra-block
// MVCC conflicts (first writer wins, matching real Fabric semantics).
func (l *ledger) process(ctx context.Context, env []*common.Envelope) error {
	// the number for the current block, and the header hash it chains onto
	// (process is only called synchronously).
	blockNum, prevHash := l.next()

	// create block in fabric format just so we can parse it again. The status code in txFilter will be updated later.
	fbl := makeBlock(blockNum, prevHash, env)

	// get the read/write set and transaction id. We don't care about the other fields.
	bl, err := l.parser.Parse(fbl)
	if err != nil {
		return err // should not happen
	}

	// Parse already set each tx.Number to its position in the block; don't renumber
	// by slice index, which drifts whenever Parse drops a config or malformed tx.

	// Validate all transactions; updates bl.Transactions[i].Status and returns txFilter.
	txFilter, err := l.validator.Validate(&bl)
	if err != nil {
		return err
	}

	// txFilter is indexed by position in bl.Transactions, which omits any transaction
	// Parse dropped, so scatter the statuses back to their block positions rather than
	// copying wholesale. Dropped positions keep their zero (unspecified) status.
	blockFilter := fbl.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER]
	for i, status := range txFilter {
		blockFilter[bl.Transactions[i].Number] = status
	}

	// commit them to the ledger
	return l.commit(ctx, bl, fbl)
}

// makeBlock creates a minimal block that chains onto prevHash, which is the header
// hash of block blockNum-1 (nil for genesis).
//
// The header carries the same two hashes a real ledger computes, the way Fabric's own
// blockledger.CreateNextBlock does it: DataHash over the marshaled envelopes and
// PreviousHash over the preceding header. Clients that walk the chain or recompute a
// block hash therefore see a consistent chain rather than placeholders. Block metadata
// is deliberately outside DataHash in Fabric, so filling in the txFilter afterwards
// does not invalidate the header.
func makeBlock(blockNum uint64, prevHash []byte, envs []*common.Envelope) *common.Block {
	data := make([][]byte, len(envs))
	for i, env := range envs {
		b, _ := proto.Marshal(env)
		data[i] = b
	}
	blockData := &common.BlockData{Data: data}

	return &common.Block{
		Header: &common.BlockHeader{
			Number:       blockNum,
			PreviousHash: prevHash,
			// ComputeBlockDataHash rather than BlockDataHash: the latter first
			// insists the transactions are well formed, which rejects the empty
			// blocks cutBlock produces and fabrictest's placeholder signatures.
			DataHash: protoutil.ComputeBlockDataHash(blockData),
		},
		// Data contains the original transactions.
		Data: blockData,
		// Metadata: signatures, last_config [deprecated], transactions_filter.
		// Pre-allocate the txFilter so we can copy in the actual statuses later.
		Metadata: &common.BlockMetadata{Metadata: [][]byte{{}, {}, make([]byte, len(envs))}},
	}
}

// commit adds a block and applies the writes to our own world state, if any.
func (l *ledger) commit(ctx context.Context, bl blocks.Block, fbl *common.Block) error {
	if l.db != nil {
		if err := l.db.UpdateWorldState(ctx, bl); err != nil {
			return err
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.blocks = append(l.blocks, fbl)

	// Notify under the same lock as close(), so a commit racing with Stop()
	// either completes before subs is nilled and its channels closed, or sees
	// the post-close state (nil subs, zero iterations) — never a half-closed one.
	for _, ch := range l.subs {
		select {
		case ch <- fbl:
		default:
		}
	}
	return nil
}

// truncate drops every block after num, so the next block takes number num+1.
// Only allowed when world state is external (db == nil): its owner rewinds it,
// as must any other subscriber.
func (l *ledger) truncate(num uint64) error {
	if l.db != nil {
		return errors.New("revert requires an external world state (db1)")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if num >= uint64(len(l.blocks)) {
		return fmt.Errorf("cannot revert to block %d: height is %d", num, len(l.blocks))
	}
	l.blocks = l.blocks[:num+1]
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

// height is the block height: the number of blocks in the chain, counting the
// genesis block at 0. As in Fabric, the next block to be cut takes this number.
func (l *ledger) height() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return uint64(len(l.blocks))
}

// next reports the number the next block takes and the header hash it must chain
// onto. Both come from one critical section so a block can never be numbered off
// one tip and chained onto another.
func (l *ledger) next() (uint64, []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// blocks is never empty: newLedger seeds genesis and truncate keeps it.
	return uint64(len(l.blocks)), protoutil.BlockHeaderHash(l.blocks[len(l.blocks)-1].Header)
}

// chainInfo reports what a real ledger reports for the chain tip: the height, the
// tip block's own header hash, and the hash of the block before it (empty while
// genesis is still the tip).
func (l *ledger) chainInfo() (height uint64, currentHash, previousHash []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	tip := l.blocks[len(l.blocks)-1].Header
	return uint64(len(l.blocks)), protoutil.BlockHeaderHash(tip), tip.PreviousHash
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
	if l.db != nil {
		l.db.Close() //nolint:errcheck,gosec // Best-effort cleanup of the in-memory test ledger.
	}
}
