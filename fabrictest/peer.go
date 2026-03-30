/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"errors"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

func newTestPeer(ledger *ledger) *testPeer {
	return &testPeer{ledger: ledger}
}

type testPeer struct {
	ledger *ledger
}

// parseStartBlock extracts the requested start block number from the seek envelope.
// Returns 0 (oldest) on any parse error, math.MaxUint64 for SeekNewest (handled by caller).
func parseStartBlock(env *common.Envelope) (uint64, bool) {
	pl := &common.Payload{}
	if err := proto.Unmarshal(env.Payload, pl); err != nil {
		return 0, false
	}
	si := &orderer.SeekInfo{}
	if err := proto.Unmarshal(pl.Data, si); err != nil {
		return 0, false
	}
	if si.Start == nil {
		return 0, false
	}
	switch t := si.Start.Type.(type) {
	case *orderer.SeekPosition_Oldest:
		return 0, false
	case *orderer.SeekPosition_Specified:
		return t.Specified.Number, false
	case *orderer.SeekPosition_Newest:
		return 0, true // caller uses current height
	default:
		return 0, false
	}
}

// Deliver first requires an Envelope of type ab.DELIVER_SEEK_INFO with
// Payload data as a marshaled orderer.SeekInfo message,
// then a stream of block replies is received.
func (p *testPeer) Deliver(stream peer.Deliver_DeliverServer) error {
	env, err := stream.Recv()
	if err != nil {
		return err
	}

	startBlock, newest := parseStartBlock(env)

	// Register as a subscriber and get a snapshot of already-committed blocks.
	// Both happen under the ledger lock so no blocks are lost between the two.
	existing, sub := p.ledger.subscribe()

	// For SeekNewest, start from the current tip (no historical replay).
	if newest {
		startBlock = uint64(len(existing))
	}

	// Replay historical blocks starting from startBlock.
	for _, block := range existing {
		if block.Header.Number < startBlock {
			continue
		}
		resp := &peer.DeliverResponse{
			Type: &peer.DeliverResponse_Block{Block: block},
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}

	// Stream new blocks as they are committed.
	for block := range sub {
		if block.Header.Number < startBlock {
			continue
		}
		resp := &peer.DeliverResponse{
			Type: &peer.DeliverResponse_Block{Block: block},
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}

	return nil
}

// DeliverFiltered first requires an Envelope of type ab.DELIVER_SEEK_INFO with
// Payload data as a marshaled orderer.SeekInfo message,
// then a stream of **filtered** block replies is received
func (p *testPeer) DeliverFiltered(peer.Deliver_DeliverFilteredServer) error {
	return errors.New("not implemented")
}

// DeliverWithPrivateData first requires an Envelope of type ab.DELIVER_SEEK_INFO with
// Payload data as a marshaled orderer.SeekInfo message,
// then a stream of block and private data replies is received
func (p *testPeer) DeliverWithPrivateData(grpc.BidiStreamingServer[common.Envelope, peer.DeliverResponse]) error {
	return errors.New("not implemented")
}
