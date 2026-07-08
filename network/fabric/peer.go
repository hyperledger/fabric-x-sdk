/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	peerpb "github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/blocks/fabric"
	"github.com/hyperledger/fabric-x-sdk/network"
	"google.golang.org/protobuf/proto"
)

// NewPeer dials a classic Fabric peer and binds it to the given channel and signer.
func NewPeer(conf network.PeerConf, channel string, signer sdk.Signer) (*Peer, error) {
	peer, err := network.NewPeer(conf)
	if err != nil {
		return nil, err
	}
	return &Peer{
		Peer:           peer,
		channel:        channel,
		signer:         signer,
		endorserClient: peerpb.NewEndorserClient(peer.Connection()),
	}, nil
}

// Peer is a channel-bound client for a classic Fabric peer.
type Peer struct {
	*network.Peer
	channel        string
	signer         sdk.Signer
	endorserClient peerpb.EndorserClient
}

// SubscribeBlocks streams blocks from startBlock, invoking processor for each one.
func (p *Peer) SubscribeBlocks(ctx context.Context, startBlock uint64, processor network.BlockProcessor) error {
	return p.Peer.SubscribeBlocks(ctx, p.channel, startBlock, p.signer, processor)
}

// ProcessProposal sends a proposal to the peer and returns the response.
// All non-nil responses are returned as-is regardless of status code — a 400
// or 500 is a valid signed reply from the endorser and the caller decides how
// to handle it. Only a nil Response field (malformed reply) is treated as an error.
func (p *Peer) ProcessProposal(ctx context.Context, signedProp *peerpb.SignedProposal) (*peerpb.ProposalResponse, error) {
	resp, err := p.endorserClient.ProcessProposal(ctx, signedProp)
	if err != nil {
		return nil, fmt.Errorf("peer: %w", err)
	}
	if resp.Response == nil {
		return nil, fmt.Errorf("peer returned nil response")
	}
	return resp, nil
}

// BlockHeight returns the current block height of the channel by querying the QSCC system chaincode.
func (p *Peer) BlockHeight(ctx context.Context) (uint64, error) {
	prop, err := NewSignedProposal(p.signer, p.channel, "qscc", [][]byte{[]byte("GetChainInfo"), []byte(p.channel)})
	if err != nil {
		return 0, err
	}
	res, err := p.ProcessProposal(ctx, prop)
	if err != nil {
		return 0, err
	}

	info := &common.BlockchainInfo{}
	if err := proto.Unmarshal(res.Response.Payload, info); err != nil {
		return 0, err
	}

	return info.Height, nil
}

// NewSynchronizer creates a Synchronizer that fetches classic Fabric blocks and dispatches
// them to the provided handlers using the Fabric block format.
func NewSynchronizer(db network.BlockHeightReader, channel string, conf network.PeerConf, signer sdk.Signer, logger sdk.Logger, handlers ...blocks.BlockHandler) (*network.Synchronizer, error) {
	peer, err := NewPeer(conf, channel, signer)
	if err != nil {
		return nil, err
	}

	return network.NewSynchronizer(
		db,
		peer,
		blocks.NewProcessor(fabric.NewBlockParser(logger), handlers),
		logger,
	)
}

// NewSignedProposal creates a new signed proposal to be submitted to a Fabric peer or endorser.
func NewSignedProposal(signer sdk.Signer, channel, namespace string, args [][]byte) (*peerpb.SignedProposal, error) {
	creator, err := signer.Serialize()
	if err != nil {
		return nil, err
	}

	nonce := mustNonce()
	proposal, _, err := protoutil.CreateChaincodeProposalWithTxIDNonceAndTransient(
		protoutil.ComputeTxID(nonce, creator),
		common.HeaderType_ENDORSER_TRANSACTION,
		channel,
		&peerpb.ChaincodeInvocationSpec{
			ChaincodeSpec: &peerpb.ChaincodeSpec{
				Type: peerpb.ChaincodeSpec_CAR,
				ChaincodeId: &peerpb.ChaincodeID{
					Name: namespace,
				},
				Input: &peerpb.ChaincodeInput{
					Args: args,
				},
			},
		},
		nonce,
		creator,
		nil,
	)
	if err != nil {
		return nil, err
	}

	return protoutil.GetSignedProposal(proposal, signer)
}

// EndorsementClient sends a proposal to one or more peers in parallel and collects
// their signed responses into an sdk.Endorsement.
type EndorsementClient struct {
	peers   []*Peer
	signer  sdk.Signer
	channel string
}

// NewEndorsementClient dials all configured peers and returns a client ready to endorse transactions.
func NewEndorsementClient(config []network.PeerConf, signer sdk.Signer, channel string) (*EndorsementClient, error) {
	if len(config) == 0 {
		return nil, fmt.Errorf("no peers configured")
	}
	peers := make([]*Peer, len(config))
	for i, c := range config {
		var err error
		peers[i], err = NewPeer(c, channel, signer)
		if err != nil {
			return nil, err
		}
	}
	return &EndorsementClient{
		peers:   peers,
		signer:  signer,
		channel: channel,
	}, nil
}

// ExecuteTransaction sends args to all configured peers in parallel, collects their
// responses, and returns an sdk.Endorsement. Returns an error if any peer fails.
func (ec *EndorsementClient) ExecuteTransaction(ctx context.Context, namespace string, args [][]byte) (sdk.Endorsement, error) {
	prop, err := NewSignedProposal(ec.signer, ec.channel, namespace, args)
	if err != nil {
		return sdk.Endorsement{}, err
	}

	type result struct {
		resp *peerpb.ProposalResponse
		err  error
	}
	results := make([]result, len(ec.peers))
	var wg sync.WaitGroup
	for i, p := range ec.peers {
		wg.Go(func() {
			resp, err := p.ProcessProposal(ctx, prop)
			results[i] = result{resp, err}
		})
	}
	wg.Wait()

	responses := make([]*peerpb.ProposalResponse, 0, len(ec.peers))
	for _, r := range results {
		if r.err != nil {
			return sdk.Endorsement{}, fmt.Errorf("peer endorsement: %w", r.err)
		}
		responses = append(responses, r.resp)
	}

	proposal, err := protoutil.UnmarshalProposal(prop.ProposalBytes)
	if err != nil {
		return sdk.Endorsement{}, err
	}
	return sdk.Endorsement{Proposal: proposal, Responses: responses}, nil
}

// Close closes all peer connections.
func (ec *EndorsementClient) Close() error {
	var errs []error
	for _, p := range ec.peers {
		if err := p.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func mustNonce() []byte {
	key := make([]byte, 24)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return key
}
