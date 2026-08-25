/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
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
	committerpb.UnimplementedNotifierServer
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
		startBlock = existing[len(existing)-1].Header.Number
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

// StreamAllTransactions implements the Fabric-X Notifier service: it streams a
// TxEventBatch for every block committed after subscription, honoring
// StreamAllRequest's namespace (OR match) and status filters as well as the
// include_* flags. Like the real committer's Notifier, this is a real-time feed
// with no historical replay.
//
// Transactions are decoded as Fabric-X applicationpb.Tx; blocks from a "fabric"
// (classic) fabrictest network use a different channel header type and are
// naturally skipped rather than misinterpreted.
func (p *testPeer) StreamAllTransactions(req *committerpb.StreamAllRequest, stream committerpb.Notifier_StreamAllTransactionsServer) error {
	_, sub := p.ledger.subscribe()

	for block := range sub {
		batch := buildTxEventBatch(block, req)
		if batch == nil {
			continue
		}
		if err := stream.Send(batch); err != nil {
			return err
		}
	}

	return nil
}

// buildTxEventBatch decodes block into a TxEventBatch filtered per req.
// It returns nil if the block has no events matching the filters.
func buildTxEventBatch(block *common.Block, req *committerpb.StreamAllRequest) *committerpb.TxEventBatch {
	txFilter := block.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER]

	var events []*committerpb.TxEvent
	for txNum, envBytes := range block.Data.Data {
		env := &common.Envelope{}
		if err := proto.Unmarshal(envBytes, env); err != nil {
			continue
		}
		pl := &common.Payload{}
		if err := proto.Unmarshal(env.Payload, pl); err != nil {
			continue
		}
		if pl.Header == nil {
			continue // malformed: a payload without a header carries no channel header
		}
		chdr := &common.ChannelHeader{}
		if err := proto.Unmarshal(pl.Header.ChannelHeader, chdr); err != nil {
			continue
		}
		if chdr.Type != int32(common.HeaderType_MESSAGE) {
			continue // config transaction, or a non-Fabric-X (classic fabric) transaction
		}
		ptx := &applicationpb.Tx{}
		if err := proto.Unmarshal(pl.Data, ptx); err != nil {
			continue
		}

		status := committerpb.Status(txFilter[txNum])
		if !statusMatches(status, req.FilterStatus) {
			continue
		}

		namespaces, touched := filterNamespaces(ptx.Namespaces, req.FilterNamespaces)
		if !touched {
			continue
		}

		event := &committerpb.TxEvent{
			Ref: &committerpb.TxRef{
				BlockNum: block.Header.Number,
				TxNum:    uint32(txNum),
				TxId:     chdr.TxId,
			},
			Status: status,
		}
		if req.IncludeReadWriteSets {
			event.Namespaces = namespaces
		}
		if req.IncludeEndorsements {
			event.Endorsements = ptx.Endorsements
		}
		if req.IncludeMetadata {
			event.Metadata = ptx.Metadata
		}

		events = append(events, event)
	}

	if len(events) == 0 {
		return nil
	}
	return &committerpb.TxEventBatch{BlockNumber: block.Header.Number, Events: events}
}

// statusMatches reports whether status passes filter (OR match). An empty filter matches everything.
func statusMatches(status committerpb.Status, filter []committerpb.Status) bool {
	if len(filter) == 0 {
		return true
	}
	return slices.Contains(filter, status)
}

// filterNamespaces returns the subset of all matching filter (OR match) along with whether
// the transaction touches any of them. An empty filter matches every namespace.
func filterNamespaces(all []*applicationpb.TxNamespace, filter []string) ([]*applicationpb.TxNamespace, bool) {
	if len(filter) == 0 {
		return all, true
	}
	set := make(map[string]struct{}, len(filter))
	for _, ns := range filter {
		set[ns] = struct{}{}
	}
	var matching []*applicationpb.TxNamespace
	for _, ns := range all {
		if _, ok := set[ns.NsId]; ok {
			matching = append(matching, ns)
		}
	}
	return matching, len(matching) > 0
}

// testSigner is a minimal sdk.Signer that returns fixed bytes.
type testSigner struct{}

func (testSigner) Sign(_ []byte) ([]byte, error) { return []byte("sig"), nil }
func (testSigner) Serialize() ([]byte, error)    { return []byte("identity"), nil }
