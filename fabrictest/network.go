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
	"fmt"
	"net"
	"strconv"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
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
	PeerPort    int
	OrdererPort int
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
func Start(namespace, networkType string, cfg Config) (*Network, error) {
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
	ordererLis, oPort, err := listen(cfg.OrdererPort)
	if err != nil {
		return nil, err
	}
	peerLis, pPort, err := listen(cfg.OrdererPort)
	if err != nil {
		return nil, err
	}

	n := &Network{
		OrdererPort: oPort,
		PeerPort:    pPort,
		oSrv:        grpc.NewServer(),
		pSrv:        grpc.NewServer(),
	}

	// ledger ties it together by storing blocks and the world state db
	n.ledger = newLedger(db, parser, validator)
	n.orderer = newTestOrderer(n.ledger, cfg)

	// grpc endpoints
	testPeer := newTestPeer(n.ledger)
	orderer.RegisterAtomicBroadcastServer(n.oSrv, n.orderer)
	peer.RegisterDeliverServer(n.pSrv, testPeer)
	peer.RegisterEndorserServer(n.pSrv, testPeer)
	committerpb.RegisterBlockQueryServiceServer(n.pSrv, testPeer)

	go n.oSrv.Serve(ordererLis)
	go n.pSrv.Serve(peerLis)

	return n, nil
}

func listen(port int) (net.Listener, int, error) {
	lis, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, 0, err
	}
	_, actualPort, err := net.SplitHostPort(lis.Addr().String())
	p, err := strconv.Atoi(actualPort)
	return lis, p, err
}

func (n *Network) Stop() {
	n.oSrv.Stop()
	n.pSrv.Stop()
	n.orderer.stop()
	n.ledger.close()
}
