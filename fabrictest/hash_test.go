/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"bytes"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/protoutil"

	"github.com/hyperledger/fabric-x-sdk/state"
)

// TestBlockHeader_ChainsByPreviousHash pins the property a client walking the chain
// relies on: each delivered block's PreviousHash is the header hash of the block
// before it, and genesis chains onto nothing.
func TestBlockHeader_ChainsByPreviousHash(t *testing.T) {
	for _, networkType := range networkTypes {
		t.Run(networkType, func(t *testing.T) {
			n, err := Start(t.Context(), "basic", networkType, Config{}, nil)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}

			const cuts = 3
			for range cuts {
				if err := n.CutBlock(t.Context()); err != nil {
					t.Fatalf("CutBlock: %v", err)
				}
			}

			stream := openDeliver(t, n, seekSpecified(0))

			genesis := recvBlock(t, stream)
			if len(genesis.Header.PreviousHash) != 0 {
				t.Errorf("genesis PreviousHash: want empty, got %x", genesis.Header.PreviousHash)
			}

			prev := genesis
			for range cuts {
				block := recvBlock(t, stream)
				want := protoutil.BlockHeaderHash(prev.Header)
				if !bytes.Equal(block.Header.PreviousHash, want) {
					t.Errorf("block %d PreviousHash: got %x, want hash of block %d (%x)",
						block.Header.Number, block.Header.PreviousHash, prev.Header.Number, want)
				}
				prev = block
			}
		})
	}
}

// TestBlockHeader_DataHashCoversData pins that DataHash is computed over the block's
// own transactions, the way a real ledger does, so a client can recompute and compare
// it. Filling in the transaction filter afterwards must not disturb it: block metadata
// is outside DataHash in Fabric.
func TestBlockHeader_DataHashCoversData(t *testing.T) {
	l := newTestLedger(t)

	envs := []*common.Envelope{
		buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, simpleTx("ns")),
		buildEnvelope(t, "tx1", common.HeaderType_MESSAGE, simpleTx("ns")),
	}
	if err := l.process(t.Context(), envs); err != nil {
		t.Fatalf("process: %v", err)
	}

	blk := l.blocks[len(l.blocks)-1]
	if want := protoutil.ComputeBlockDataHash(blk.Data); !bytes.Equal(blk.Header.DataHash, want) {
		t.Errorf("DataHash: got %x, want %x", blk.Header.DataHash, want)
	}

	// Two blocks carrying different transactions must not hash alike.
	genesis := l.blocks[0]
	if bytes.Equal(genesis.Header.DataHash, blk.Header.DataHash) {
		t.Error("genesis and the application block share a DataHash")
	}
}

// TestChainInfo_MatchesDeliveredHeaders pins that the hashes GetBlockchainInfo reports
// are the same ones in the delivered headers — a client comparing the two must agree.
func TestChainInfo_MatchesDeliveredHeaders(t *testing.T) {
	n, err := Start(t.Context(), "basic", "fabric-x", Config{}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Genesis is still the tip: there is no block before it.
	if _, _, prev := n.ledger.chainInfo(); len(prev) != 0 {
		t.Errorf("PreviousBlockHash at genesis: want empty, got %x", prev)
	}

	if err := n.CutBlock(t.Context()); err != nil {
		t.Fatalf("CutBlock: %v", err)
	}

	stream := openDeliver(t, n, seekSpecified(0))
	genesis := recvBlock(t, stream)
	tip := recvBlock(t, stream)

	height, currentHash, previousHash := n.ledger.chainInfo()
	if height != 2 {
		t.Errorf("height: got %d, want 2", height)
	}
	if want := protoutil.BlockHeaderHash(tip.Header); !bytes.Equal(currentHash, want) {
		t.Errorf("CurrentBlockHash: got %x, want %x", currentHash, want)
	}
	if want := protoutil.BlockHeaderHash(genesis.Header); !bytes.Equal(previousHash, want) {
		t.Errorf("PreviousBlockHash: got %x, want %x", previousHash, want)
	}
}

// TestRevert_RechainsOntoNewTip pins that a block cut after a revert chains onto the
// block it actually follows, not onto the discarded fork.
func TestRevert_RechainsOntoNewTip(t *testing.T) {
	ext, err := state.NewWriteDB("mychannel", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	n, err := Start(t.Context(), "basic", "fabric-x", Config{}, ext.CurrentRecordGetter())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for range 3 {
		if err := n.CutBlock(t.Context()); err != nil {
			t.Fatal(err)
		}
	}

	// Rewind to block 1, so the next cut replaces block 2.
	if err := n.Revert(t.Context(), 1); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	newTip := n.ledger.blocks[1]

	if err := n.CutBlock(t.Context()); err != nil {
		t.Fatal(err)
	}

	replacement := n.ledger.blocks[2]
	if want := protoutil.BlockHeaderHash(newTip.Header); !bytes.Equal(replacement.Header.PreviousHash, want) {
		t.Errorf("PreviousHash after revert: got %x, want hash of block 1 (%x)",
			replacement.Header.PreviousHash, want)
	}
}
