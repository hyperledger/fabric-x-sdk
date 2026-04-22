/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Peer struct {
	conn   *grpc.ClientConn
	client peer.EndorserClient
}

func NewPeer(c PeerConf) (*Peer, error) {
	if err := c.TLS.Validate(); err != nil {
		return nil, fmt.Errorf("peer %s: invalid TLS config: %w", c.Address, err)
	}

	host, _, err := net.SplitHostPort(c.Address)
	if err != nil {
		return nil, fmt.Errorf("peer %s: address must contain port: %w", c.Address, err)
	}

	creds := insecure.NewCredentials()
	if c.TLS.Mode != "" && c.TLS.Mode != TLSModeNone {
		tlsCfg, err := c.TLS.LoadClientTLSConfig(host)
		if err != nil {
			return nil, fmt.Errorf("peer %s: failed to load TLS config: %w", c.Address, err)
		}
		creds = credentials.NewTLS(tlsCfg)
	}

	conn, err := grpc.NewClient(
		c.Address,
		grpc.WithTransportCredentials(creds),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: 10 * time.Second,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dial peer %s: %w", c.Address, err)
	}

	return &Peer{
		conn:   conn,
		client: peer.NewEndorserClient(conn),
	}, nil
}

// ProcessProposal sends a proposal to the peer and returns the response.
// All non-nil responses are returned as-is regardless of status code — a 400
// or 500 is a valid signed reply from the endorser and the caller decides how
// to handle it. Only a nil Response field (malformed reply) is treated as an error.
func (p *Peer) ProcessProposal(ctx context.Context, signedProp *peer.SignedProposal) (*peer.ProposalResponse, error) {
	resp, err := p.client.ProcessProposal(ctx, signedProp)
	if err != nil {
		return nil, fmt.Errorf("peer: %w", err)
	}
	if resp.Response == nil {
		return nil, fmt.Errorf("peer returned nil response")
	}
	return resp, nil
}

// BlockProcessor processes a single block.
// Returning an error will stop the subscription.
type BlockProcessor interface {
	ProcessBlock(ctx context.Context, block *common.Block) error
}

// SubscribeBlocks connects to the peer DeliverWithPrivateData service and streams blocks
// from the given starting block number, invoking the provided handler for each block.
func (p *Peer) SubscribeBlocks(ctx context.Context, channel string, startBlock uint64, signer sdk.Signer, processor BlockProcessor) error {
	deliverClient := peer.NewDeliverClient(p.conn)

	deliver, err := deliverClient.Deliver(ctx)
	if err != nil {
		return fmt.Errorf("open Deliver: %w", err)
	}
	defer deliver.CloseSend() //nolint:errcheck

	env, err := newDeliverSeekInfo(signer, channel, startBlock)
	if err != nil {
		return fmt.Errorf("build seek envelope: %w", err)
	}
	if err := deliver.Send(env); err != nil {
		return fmt.Errorf("send seek envelope: %w", err)
	}

	for {
		msg, err := deliver.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv deliver: %w", err)
		}

		switch t := msg.Type.(type) {
		case *peer.DeliverResponse_Block:
			if err := processor.ProcessBlock(ctx, t.Block); err != nil {
				return fmt.Errorf("processor: %w", err)
			}
		case *peer.DeliverResponse_Status:
			if t.Status != common.Status_SUCCESS {
				return fmt.Errorf("deliver stream ended: %s", t.Status)
			}
			return nil
		}
	}
}

// Connection returns the grpc connection to the peer.
func (p *Peer) Connection() *grpc.ClientConn {
	return p.conn
}

// Close stops the connection to the peer.
func (p *Peer) Close() error {
	return p.conn.Close()
}

// PeerConf tells the EndorsementClient or Synchronizer how to reach a peer.
type PeerConf struct {
	Address string
	TLS     TLSConfig
}

// EndorsementClient sends a proposal to one or more peers in parallel and collects
// their signed responses into an sdk.Endorsement.
type EndorsementClient struct {
	peers   []*Peer
	signer  sdk.Signer
	channel string
}

// NewEndorsementClient dials all configured peers and returns a client ready to endorse transactions.
func NewEndorsementClient(config []PeerConf, signer sdk.Signer, channel, namespace, nsVersion string) (*EndorsementClient, error) {
	if len(config) == 0 {
		return nil, fmt.Errorf("no peers configured")
	}
	peers := make([]*Peer, len(config))
	for i, c := range config {
		var err error
		peers[i], err = NewPeer(c)
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
func (ec *EndorsementClient) ExecuteTransaction(ctx context.Context, namespace, nsVersion string, args [][]byte) (sdk.Endorsement, error) {
	prop, err := NewSignedProposal(ec.signer, ec.channel, namespace, nsVersion, args)
	if err != nil {
		return sdk.Endorsement{}, err
	}

	type result struct {
		resp *peer.ProposalResponse
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

	responses := make([]*peer.ProposalResponse, 0, len(ec.peers))
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

// NewSignedProposal creates a new proposal to be submitted to a peer or endorser.
func NewSignedProposal(signer sdk.Signer, channel, namespace, nsVersion string, args [][]byte) (*peer.SignedProposal, error) {
	creator, err := signer.Serialize()
	if err != nil {
		return nil, err
	}

	nonce := mustNonce()
	proposal, _, err := protoutil.CreateChaincodeProposalWithTxIDNonceAndTransient(
		protoutil.ComputeTxID(nonce, creator),
		common.HeaderType_ENDORSER_TRANSACTION,
		channel,
		&peer.ChaincodeInvocationSpec{
			ChaincodeSpec: &peer.ChaincodeSpec{
				Type: peer.ChaincodeSpec_CAR, // FIXME: should we put some special value here?
				ChaincodeId: &peer.ChaincodeID{
					Name:    namespace,
					Version: nsVersion,
				},
				Input: &peer.ChaincodeInput{
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

// newDeliverSeekInfo returns a signed envelope that can be used to subscribe to a peer
func newDeliverSeekInfo(submitter sdk.Signer, channel string, startBlock uint64) (*common.Envelope, error) {
	signer, err := submitter.Serialize()
	if err != nil {
		return nil, err
	}
	tm := timestamppb.Now()
	tm.Nanos = 0

	channelHeader, err := proto.Marshal(&common.ChannelHeader{
		Type:      int32(common.HeaderType_DELIVER_SEEK_INFO),
		Timestamp: tm,
		ChannelId: channel,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal channel header: %w", err)
	}
	sigHeader, err := proto.Marshal(&common.SignatureHeader{
		Creator: signer, Nonce: mustNonce(),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal signature header: %w", err)
	}
	seekInfo, err := proto.Marshal(&orderer.SeekInfo{
		Start: &orderer.SeekPosition{
			Type: &orderer.SeekPosition_Specified{
				Specified: &orderer.SeekSpecified{Number: startBlock},
			},
		},
		Stop: &orderer.SeekPosition{
			Type: &orderer.SeekPosition_Specified{
				Specified: &orderer.SeekSpecified{Number: math.MaxUint64},
			},
		},
		Behavior: orderer.SeekInfo_BLOCK_UNTIL_READY,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal seek info: %w", err)
	}
	payload, err := proto.Marshal(&common.Payload{
		Header: &common.Header{
			ChannelHeader:   channelHeader,
			SignatureHeader: sigHeader,
		},
		Data: seekInfo,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	sig, err := submitter.Sign(payload)
	if err != nil {
		return nil, fmt.Errorf("sign payload: %w", err)
	}

	return &common.Envelope{
		Payload:   payload,
		Signature: sig,
	}, nil
}

// mustNonce generates 24 random bytes to be used as a nonce.
func mustNonce() []byte {
	key := make([]byte, 24)
	_, err := rand.Read(key)
	if err != nil {
		// rand.Read uses operating system APIs that are documented to never
		// return an error on all but legacy Linux systems.
		panic(err)
	}
	return key
}
