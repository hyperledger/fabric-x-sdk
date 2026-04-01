/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package testfabric provides a mock implementation of an orderer and a peer.
// It creates one transaction per block and does some very basic MVCC checks.
// The Network can be embedded in an integration test by using the OrdererAddr
// and PeerAddr.
package fabrictest

import (
	"errors"
	"net"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/grpc"

	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/blocks/fabric"
	"github.com/hyperledger/fabric-x-sdk/blocks/fabricx"
	"github.com/hyperledger/fabric-x-sdk/state"
)

// Network is a wrapper for a minimal mock Fabric.
// Embed Network in a test and use PeerAddr and OrdererAddr as endpoints.
type Network struct {
	OrdererAddr string
	PeerAddr    string
	oSrv        *grpc.Server
	pSrv        *grpc.Server
	orderer     *testOrderer
	ledger      *ledger
}

// TxParser extracts read-write sets from transaction envelopes.
// Fabric and fabric-x have the same envelope, but different payloads.
// The TxParser knows how to process them.
type TxParser interface {
	Parse(namespace string, env *common.Envelope) (blocks.ReadWriteSet, string, error)
}

// Start creates grpc servers for the peer and orderer on random ports on localhost.
func Start(namespace, networkType string, cfg BatchingConfig) (*Network, error) {
	logger := sdk.NewStdLogger("fabrictest")

	// in memory world state db
	db, err := state.NewWriteDB("mychannel", ":memory:")
	if err != nil {
		return nil, err
	}

	// some specifics for either fabric or fabric-x
	var parser blocks.BlockParser
	var validator *blocks.MVCCValidator

	switch networkType {
	case "fabric":
		parser = fabric.NewBlockParser(logger)
		validator = fabric.NewMVCCValidator(db, logger)
	case "fabric-x":
		parser = fabricx.NewBlockParser(logger)
		validator = fabricx.NewMVCCValidator(db, logger)
	default:
		return nil, errors.New("networkType must be fabric or fabric-x")
	}

	// listen on random local port
	ordererLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	peerLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	n := &Network{
		OrdererAddr: ordererLis.Addr().String(),
		PeerAddr:    peerLis.Addr().String(),
		oSrv:        grpc.NewServer(),
		pSrv:        grpc.NewServer(),
	}

	// ledger ties it together by storing blocks and the world state db
	n.ledger = newLedger(db, parser, validator)
	n.orderer = newTestOrderer(n.ledger, cfg)

	// grpc endpoints
	orderer.RegisterAtomicBroadcastServer(n.oSrv, n.orderer)
	peer.RegisterDeliverServer(n.pSrv, newTestPeer(n.ledger))

	go n.oSrv.Serve(ordererLis)
	go n.pSrv.Serve(peerLis)

	return n, nil
}

func (n *Network) Stop() {
	n.oSrv.Stop()
	n.pSrv.Stop()
	n.orderer.stop()
	n.ledger.close()
}
