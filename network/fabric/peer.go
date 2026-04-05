/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"context"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/blocks/fabric"
	"github.com/hyperledger/fabric-x-sdk/network"
	"google.golang.org/protobuf/proto"
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
	prop, err := network.NewSignedProposal(p.signer, p.channel, "qscc", "1.0", [][]byte{[]byte("GetChainInfo"), []byte(p.channel)})
	if err != nil {
		return 0, err
	}
	res, err := p.Peer.ProcessProposal(ctx, prop)
	if err != nil {
		return 0, err
	}

	info := &common.BlockchainInfo{}
	if err := proto.Unmarshal(res.Response.Payload, info); err != nil {
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
		blocks.NewProcessor(fabric.NewBlockParser(logger), handlers),
		logger,
	)
}
