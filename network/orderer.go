/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package network has clients for peers and orderers, along with simple wrappers
// to submit a transaction and to synchronize a local database with a committer.
package network

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// OrdererConf tells the submitter how to reach an orderer.
type OrdererConf struct {
	Address string
	TLS     TLSConfig
}

// NewOrderer dials an orderer and returns a client ready to broadcast transactions.
func NewOrderer(ctx context.Context, c OrdererConf) (*Orderer, error) {
	if err := c.TLS.Validate(); err != nil {
		return nil, fmt.Errorf("orderer %s: invalid TLS config: %w", c.Address, err)
	}

	host, _, err := net.SplitHostPort(c.Address)
	if err != nil {
		return nil, fmt.Errorf("orderer %s: address must contain port: %w", c.Address, err)
	}

	creds := insecure.NewCredentials()
	if c.TLS.Mode != "" && c.TLS.Mode != TLSModeNone {
		tlsCfg, err := c.TLS.LoadClientTLSConfig(host)
		if err != nil {
			return nil, fmt.Errorf("orderer %s: failed to load TLS config: %w", c.Address, err)
		}
		creds = credentials.NewTLS(tlsCfg)
	}

	conn, err := grpc.NewClient(c.Address, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial orderer %s: %w", c.Address, err)
	}

	return &Orderer{
		ctx:    ctx,
		conn:   conn,
		client: orderer.NewAtomicBroadcastClient(conn),
		addr:   c.Address,
	}, nil
}

// Orderer is a Fabric or Fabric-X orderer with persistent stream support.
type Orderer struct {
	ctx    context.Context
	conn   *grpc.ClientConn
	client orderer.AtomicBroadcastClient
	addr   string

	mu           sync.Mutex
	closed       bool
	streamCtx    context.Context
	streamCancel context.CancelFunc
	stream       orderer.AtomicBroadcast_BroadcastClient
}

// Broadcast sends a signed envelope with an endorsed EndorserTransaction for ordering.
// Uses a persistent stream with automatic recreation on failure.
// The context parameter is ignored - stream lifecycle is managed by the orderer's context.
func (o *Orderer) Broadcast(ctx context.Context, env *common.Envelope) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	if err := o.ensureStream(); err != nil {
		return err
	}
	if err := o.stream.Send(env); err == nil {
		return nil
	}

	// Send failed: force recreation and retry once.
	o.streamCancel()
	if err := o.ensureStream(); err != nil {
		return err
	}
	if err := o.stream.Send(env); err != nil {
		return fmt.Errorf("broadcast to %s: %w", o.addr, err)
	}
	return nil
}

// ensureStream creates a new broadcast stream if the current one is absent or canceled.
// Caller must hold o.mu.
func (o *Orderer) ensureStream() error {
	if o.closed {
		return fmt.Errorf("orderer %s: closed", o.addr)
	}
	if o.stream != nil && o.streamCtx.Err() == nil {
		return nil
	}
	if o.streamCancel != nil {
		o.streamCancel()
	}
	o.stream = nil
	o.streamCtx, o.streamCancel = context.WithCancel(o.ctx)
	stream, err := o.client.Broadcast(o.streamCtx)
	if err != nil {
		o.streamCancel()
		return fmt.Errorf("open broadcast stream to %s: %w", o.addr, err)
	}
	o.stream = stream
	cancel := o.streamCancel
	go func() {
		defer cancel()
		for {
			if _, err := stream.Recv(); err != nil {
				return
			}
		}
	}()
	return nil
}

// Close closes the connection to the orderer.
func (o *Orderer) Close() error {
	o.mu.Lock()
	o.closed = true
	if o.streamCancel != nil {
		o.streamCancel()
	}
	o.mu.Unlock()
	return o.conn.Close()
}
