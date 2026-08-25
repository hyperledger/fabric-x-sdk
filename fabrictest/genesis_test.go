/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
)

// networkTypes are the two backends fabrictest emulates; block numbering must be
// identical across both.
var networkTypes = []string{"fabric", "fabric-x"}

// seekEnvelope builds the DELIVER_SEEK_INFO envelope that Deliver expects.
func seekEnvelope(t *testing.T, start *orderer.SeekPosition) *common.Envelope {
	t.Helper()
	seekBytes, err := proto.Marshal(&orderer.SeekInfo{Start: start})
	if err != nil {
		t.Fatalf("marshal SeekInfo: %v", err)
	}
	payloadBytes, err := proto.Marshal(&common.Payload{Data: seekBytes})
	if err != nil {
		t.Fatalf("marshal Payload: %v", err)
	}
	return &common.Envelope{Payload: payloadBytes}
}

// openDeliver dials the network's peer and starts a Deliver stream at start.
// The stream is bounded by a timeout so a missing block fails instead of hanging.
func openDeliver(t *testing.T, n *Network, start *orderer.SeekPosition) peer.Deliver_DeliverClient {
	t.Helper()
	conn, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", n.PeerPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial peer: %v", err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	stream, err := peer.NewDeliverClient(conn).Deliver(ctx)
	if err != nil {
		t.Fatalf("open deliver stream: %v", err)
	}
	if err := stream.Send(seekEnvelope(t, start)); err != nil {
		t.Fatalf("send seek info: %v", err)
	}
	return stream
}

// recvBlock reads the next block off a Deliver stream.
func recvBlock(t *testing.T, stream peer.Deliver_DeliverClient) *common.Block {
	t.Helper()
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv block: %v", err)
	}
	block := resp.GetBlock()
	if block == nil {
		t.Fatalf("deliver response carried no block: %v", resp)
	}
	return block
}

func seekSpecified(num uint64) *orderer.SeekPosition {
	return &orderer.SeekPosition{
		Type: &orderer.SeekPosition_Specified{Specified: &orderer.SeekSpecified{Number: num}},
	}
}

// TestGenesisBlock_DeliveredFromZero pins that both backends start at block 0 with a
// config block, and that the first application block is 1 — matching Fabric, where
// block 0 is genesis, and Fabric-X, whose mock orderer emits a CONFIG block at 0.
func TestGenesisBlock_DeliveredFromZero(t *testing.T) {
	for _, networkType := range networkTypes {
		t.Run(networkType, func(t *testing.T) {
			n, err := Start(t.Context(), "basic", networkType, Config{}, nil)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}

			if got := n.ledger.height(); got != 1 {
				t.Errorf("height of a fresh ledger: got %d, want 1 (genesis only)", got)
			}

			stream := openDeliver(t, n, seekSpecified(0))

			genesis := recvBlock(t, stream)
			if genesis.Header.Number != 0 {
				t.Errorf("first delivered block: got number %d, want 0", genesis.Header.Number)
			}
			if !protoutil.IsConfigBlock(genesis) {
				t.Error("block 0 is not recognized as a config block by protoutil.IsConfigBlock")
			}

			// The first application block follows genesis at 1.
			if err := n.CutBlock(t.Context()); err != nil {
				t.Fatalf("CutBlock: %v", err)
			}
			if got := recvBlock(t, stream).Header.Number; got != 1 {
				t.Errorf("first application block: got number %d, want 1", got)
			}
		})
	}
}

// TestGenesisBlock_NotReportedAsTransaction checks the config block stays out of the
// world state and the notifier: both parsers drop non-MESSAGE envelopes, so genesis
// must not surface as a transaction.
func TestGenesisBlock_NotReportedAsTransaction(t *testing.T) {
	for _, networkType := range networkTypes {
		t.Run(networkType, func(t *testing.T) {
			n, err := Start(t.Context(), "basic", networkType, Config{}, nil)
			if err != nil {
				t.Fatalf("Start: %v", err)
			}

			genesis := n.ledger.blocks[0]
			bl, err := n.ledger.parser.Parse(genesis)
			if err != nil {
				t.Fatalf("parse genesis: %v", err)
			}
			if len(bl.Transactions) != 0 {
				t.Errorf("genesis parsed to %d transactions, want 0", len(bl.Transactions))
			}

			// BlockNumber reports 0 for a fresh store, so the synchronizer starts at 0
			// and replays genesis as a no-op, exactly as against a real network.
			lastBlock, err := n.ledger.db.BlockNumber(t.Context())
			if err != nil {
				t.Fatalf("BlockNumber: %v", err)
			}
			if lastBlock != 0 {
				t.Errorf("last processed block on a fresh store: got %d, want 0", lastBlock)
			}
		})
	}
}

// TestDeliver_SeekNewestStartsAtTip guards the tip calculation. Deriving it from
// len(existing) skips past the newest block once genesis occupies position 0.
func TestDeliver_SeekNewestStartsAtTip(t *testing.T) {
	n, err := Start(t.Context(), "basic", "fabric-x", Config{}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	const cuts = 3
	for range cuts {
		if err := n.CutBlock(t.Context()); err != nil {
			t.Fatalf("CutBlock: %v", err)
		}
	}

	tip := n.ledger.height() - 1
	if tip != cuts {
		t.Fatalf("tip after %d cuts: got %d, want %d", cuts, tip, cuts)
	}

	stream := openDeliver(t, n, &orderer.SeekPosition{
		Type: &orderer.SeekPosition_Newest{Newest: &orderer.SeekNewest{}},
	})

	if got := recvBlock(t, stream).Header.Number; got != tip {
		t.Errorf("SeekNewest delivered block %d, want the tip %d", got, tip)
	}
}
