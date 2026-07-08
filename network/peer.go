/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Peer is a gRPC client for a Fabric peer or Fabric-X committer sidecar.
// Use the protocol-specific constructors in network/fabric or network/fabricx for a
// higher-level API that pre-binds a channel and signer.
type Peer struct {
	conn *grpc.ClientConn
}

// NewPeer dials the peer at the address in conf and returns a client.
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

	return &Peer{conn: conn}, nil
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

// PeerConf tells the Synchronizer how to reach a peer.
type PeerConf struct {
	Address string
	TLS     TLSConfig
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
