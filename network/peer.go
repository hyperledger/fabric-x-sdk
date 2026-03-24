/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Peer struct {
	conn   *grpc.ClientConn
	client peer.EndorserClient
}

func NewPeer(addr string, tlsPem []byte) (*Peer, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("peer address [%s] must contain port: %w", addr, err)
	}

	creds := insecure.NewCredentials()
	if len(tlsPem) > 0 {
		roots := x509.NewCertPool()
		if ok := roots.AppendCertsFromPEM(tlsPem); !ok {
			return nil, fmt.Errorf("failed to append peer TLS cert")
		}
		creds = credentials.NewTLS(&tls.Config{
			RootCAs:    roots,
			ServerName: host,
		})
	}

	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(creds),
		grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: 10 * time.Second,
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dial peer: %w", err)
	}

	return &Peer{
		conn:   conn,
		client: peer.NewEndorserClient(conn),
	}, nil
}

// Query sends a proposal to the peer and returns the response.
func (p *Peer) Query(ctx context.Context, signedProp *peer.SignedProposal) (*peer.ProposalResponse, error) {
	resp, err := p.client.ProcessProposal(ctx, signedProp)
	if err != nil {
		return nil, fmt.Errorf("peer: %w", err)
	}
	// Check response status
	if resp.Response.Status != 200 {
		return nil, fmt.Errorf("invocation failed with status %d: %s", resp.Response.Status, resp.Response.Message)
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
			st, ok := status.FromError(err)
			if ok && st.Code() == codes.Canceled {
				// Peer connection is closing from our side.
				return nil
			} else {
				return fmt.Errorf("recv deliver: %w", err)
			}
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

func (p *Peer) Close() error {
	return p.conn.Close()
}

// NewSignedProposal creates a new proposal to be submitted to a peer or endorser.
func NewSignedProposal(signer sdk.Signer, channel, namespace, nsVersion string, args [][]byte, nonce []byte) (*peer.SignedProposal, error) {
	if nonce == nil {
		nonce = mustNonce()
	}

	creator, err := signer.Serialize()
	if err != nil {
		return nil, err
	}

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

	payload := protoutil.MarshalOrPanic(&common.Payload{
		Header: &common.Header{
			ChannelHeader: protoutil.MarshalOrPanic(&common.ChannelHeader{
				Type:      int32(common.HeaderType_DELIVER_SEEK_INFO),
				Timestamp: tm,
				ChannelId: channel,
			}),
			SignatureHeader: protoutil.MarshalOrPanic(&common.SignatureHeader{
				Creator: signer, Nonce: mustNonce(),
			}),
		},
		Data: protoutil.MarshalOrPanic(&orderer.SeekInfo{
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
		}),
	})

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
