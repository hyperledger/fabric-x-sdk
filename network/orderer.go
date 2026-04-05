/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package network has clients for peers and orderers, along with simple wrappers
// to submit a transaction and to synchronize a local database with a committer.
package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TxPackager converts endorsements to an envelope that can be submitted to a Fabric-like ordering service.
type TxPackager interface {
	PackageTx(sdk.Endorsement) (*common.Envelope, error)
}

// OrdererConf tells the submitter how to reach an orderer.
type OrdererConf struct {
	Address string
	TLS     TLSConfig
}

func NewOrderer(c OrdererConf) (*Orderer, error) {
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
		conn:   conn,
		client: orderer.NewAtomicBroadcastClient(conn),
		addr:   c.Address,
	}, nil
}

// FabricSubmitter can be used on both Fabric and Fabric-X.
type FabricSubmitter struct {
	orderers []*Orderer
	packager TxPackager
	logger   sdk.Logger
	// waitAfterSubmit is a temporary patch to wait for finality, until we have a proper finality listener. FIXME
	waitAfterSubmit time.Duration
}

func NewSubmitter(config []OrdererConf, packager TxPackager, waitAfterSubmit time.Duration, logger sdk.Logger) (*FabricSubmitter, error) {
	if len(config) == 0 {
		return nil, errors.New("no orderers configured")
	}

	orderers := make([]*Orderer, len(config))
	for i, cfg := range config {
		if o, err := NewOrderer(cfg); err != nil {
			return nil, err
		} else {
			orderers[i] = o
		}
	}

	return &FabricSubmitter{
		orderers:        orderers,
		packager:        packager,
		logger:          logger,
		waitAfterSubmit: waitAfterSubmit,
	}, nil
}

// Submit to each of the registered orderers, returns an error if more than half errored.
func (s FabricSubmitter) Submit(ctx context.Context, end sdk.Endorsement) error {
	env, err := s.packager.PackageTx(end)
	if err != nil {
		return fmt.Errorf("package proposal: %w", err)
	}

	var wg sync.WaitGroup
	var errs int32
	for i, o := range s.orderers {
		wg.Go(func() {
			if err := o.Broadcast(ctx, env); err != nil {
				s.logger.Warnf("orderer %d (%s): broadcast failed: %v", i, o.addr, err)
				atomic.AddInt32(&errs, 1)
			}
		})
	}
	wg.Wait()

	if int(errs)*2 > len(s.orderers) {
		return errors.New("error broadcasting")
	}

	if s.waitAfterSubmit > 0 {
		time.Sleep(s.waitAfterSubmit)
	}

	return nil
}

// Close closes the connections to all orderers.
func (s *FabricSubmitter) Close() error {
	var errs []error
	for _, o := range s.orderers {
		if err := o.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Orderer is a Fabric or Fabric-X orderer.
type Orderer struct {
	conn   *grpc.ClientConn
	client orderer.AtomicBroadcastClient
	addr   string
}

// Broadcast sends a signed envelope with an endorsed EndorserTransaction for ordering.
func (o *Orderer) Broadcast(ctx context.Context, env *common.Envelope) error {
	stream, err := o.client.Broadcast(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	if err := stream.Send(env); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	resp, err := stream.Recv()
	if err != nil {
		return err
	}
	if resp.Status != common.Status_SUCCESS {
		return fmt.Errorf("orderer rejected: %s", resp.Status.String())
	}
	return nil
}

func (o *Orderer) Close() error {
	return o.conn.Close()
}
