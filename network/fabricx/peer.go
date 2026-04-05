/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"context"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/blocks/fabricx"
	"github.com/hyperledger/fabric-x-sdk/network"
	"google.golang.org/protobuf/types/known/emptypb"
)

func NewPeer(conf network.PeerConf, channel string, signer sdk.Signer) (*Peer, error) {
	peer, err := network.NewPeer(conf)
	if err != nil {
		return nil, err
	}
	return &Peer{Peer: peer, channel: channel, signer: signer}, nil
}

type Peer struct {
	*network.Peer
	channel string
	signer  sdk.Signer
}

func (p *Peer) SubscribeBlocks(ctx context.Context, startBlock uint64, processor network.BlockProcessor) error {
	return p.Peer.SubscribeBlocks(ctx, p.channel, startBlock, p.signer, processor)
}

func (p *Peer) BlockHeight(ctx context.Context) (uint64, error) {
	client := committerpb.NewBlockQueryServiceClient(p.Connection())
	info, err := client.GetBlockchainInfo(ctx, &emptypb.Empty{})
	if err != nil {
		return 0, err
	}
	return info.Height, nil
}

func NewSynchronizer(db network.BlockHeightReader, channel string, conf network.PeerConf, signer sdk.Signer, logger sdk.Logger, handlers ...blocks.BlockHandler) (*network.Synchronizer, error) {
	peer, err := NewPeer(conf, channel, signer)
	if err != nil {
		return nil, err
	}

	return network.NewSynchronizer(
		db,
		peer,
		blocks.NewProcessor(fabricx.NewBlockParser(logger), handlers),
		logger,
	)
}
