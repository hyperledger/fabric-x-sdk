/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"context"
	"errors"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric-x-sdk/endorsement/fabric"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func newTestPeer(ledger *ledger) *testPeer {
	return &testPeer{
		ledger:  ledger,
		builder: fabric.NewEndorsementBuilder(&testSigner{}),
	}
}

type testPeer struct {
	ledger  *ledger
	builder endorsement.Builder
	committerpb.UnimplementedBlockQueryServiceServer
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

// -- Fabric

// ProcessProposal implements the Fabric Endorser API for processing proposals.
// It only handles qscc GetChainInfo requests to return blockchain height.
func (p *testPeer) ProcessProposal(ctx context.Context, prop *peer.SignedProposal) (*peer.ProposalResponse, error) {
	// Parse the proposal
	inv, err := endorsement.Parse(prop, time.Now())
	if err != nil {
		return nil, err
	}

	// Check if this is a qscc GetChainInfo request
	if inv.CCID.Name != "qscc" || len(inv.Args) == 0 || string(inv.Args[0]) != "GetChainInfo" {
		return nil, errors.New("only qscc GetChainInfo is supported in fabrictest")
	}

	// Return ProposalResponse with the ledger height and mocked hashes as payload, just as a Fabric peer would respond.
	return p.builder.Endorse(inv, endorsement.ExecutionResult{
		Status: 200,
		Payload: protoutil.MarshalOrPanic(&common.BlockchainInfo{
			Height:            p.ledger.height(),
			CurrentBlockHash:  []byte("current-block-hash"),
			PreviousBlockHash: []byte("previous-block-hash"),
		}),
	})
}

// Fabric-X

// GetBlockchainInfo implements the Fabric-X BlockQueryService for getting blockchain height
func (p *testPeer) GetBlockchainInfo(ctx context.Context, _ *emptypb.Empty) (*common.BlockchainInfo, error) {
	height := p.ledger.height()
	return &common.BlockchainInfo{
		Height: height,
	}, nil
}

// GetBlockByNumber implements the Fabric-X BlockQueryService for getting a specific block
func (p *testPeer) GetBlockByNumber(ctx context.Context, req *committerpb.BlockNumber) (*common.Block, error) {
	return nil, errors.New("GetBlockByNumber not implemented in fabrictest")
}

// GetBlockByTxID implements the Vabric-X BlockQueryService for getting a block by transaction ID
func (p *testPeer) GetBlockByTxID(ctx context.Context, req *committerpb.TxID) (*common.Block, error) {
	return nil, errors.New("GetBlockByTxID not implemented in fabrictest")
}

// GetTxByID implements the Dabric-X BlockQueryService for getting a transaction by ID
func (p *testPeer) GetTxByID(ctx context.Context, req *committerpb.TxID) (*common.Envelope, error) {
	return nil, errors.New("GetTxByID not implemented in fabrictest")
}

// testSigner is a minimal sdk.Signer that returns fixed bytes.
type testSigner struct{}

func (testSigner) Sign(_ []byte) ([]byte, error) { return []byte("sig"), nil }
func (testSigner) Serialize() ([]byte, error)    { return []byte("identity"), nil }
