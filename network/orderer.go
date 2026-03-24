/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package network has clients for peers and orderers, along with simple wrappers
// to submit a transaction and to synchronize a local database with a committer.
package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
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

// FabricSubmitter can be used on both Fabric and Fabric-X.
type FabricSubmitter struct {
	orderers []*Orderer
	packager TxPackager
	// waitAfterSubmit is a temporary patch to wait for finality, until we have a proper finality listener. FIXME
	waitAfterSubmit time.Duration
}

// OrdererConf tells the submitter how to reach an orderer.
type OrdererConf struct {
	Address string
	TLSPath string
}

func NewSubmitter(config []OrdererConf, packager TxPackager, waitAfterSubmit time.Duration) (*FabricSubmitter, error) {
	if len(config) == 0 {
		return nil, errors.New("no orderers configured")
	}

	or := make([]*Orderer, len(config))
	for i, o := range config {
		var pem []byte
		var err error
		if len(o.TLSPath) > 0 {
			pem, err = os.ReadFile(o.TLSPath)
			if err != nil {
				return nil, err
			}
		}
		o, err := NewOrderer(o.Address, pem)
		if err != nil {
			return nil, err
		}
		or[i] = o
	}

	return &FabricSubmitter{
		orderers:        or,
		packager:        packager,
		waitAfterSubmit: waitAfterSubmit,
	}, nil
}

// Submit to each of the registered orderers, returns an error if more than half errored.
func (s FabricSubmitter) Submit(_ context.Context, end sdk.Endorsement) error {
	env, err := s.packager.PackageTx(end)
	if err != nil {
		return fmt.Errorf("package proposal: %w", err)
	}

	var wg sync.WaitGroup
	var errs int32
	for _, o := range s.orderers {
		wg.Go(func() {
			if err := o.Broadcast(env); err != nil {
				//log.Error(fmt.Sprintf("orderer%d: %s", n, err.Error()))
				atomic.AddInt32(&errs, 1)
			}
		})
	}
	wg.Wait()

	if int(errs) > (len(s.orderers)/2)+1 {
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
	stream orderer.AtomicBroadcast_BroadcastClient
	ctx    context.Context
	cancel context.CancelFunc
}

func NewOrderer(addr string, tlsPem []byte) (*Orderer, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("orderer address [%s] must contain port: %w", addr, err)
	}
	creds := insecure.NewCredentials()
	if len(tlsPem) > 0 {
		roots := x509.NewCertPool()
		if ok := roots.AppendCertsFromPEM(tlsPem); !ok {
			return nil, fmt.Errorf("failed to append orderer TLS cert")
		}
		creds = credentials.NewTLS(&tls.Config{
			RootCAs:    roots,
			ServerName: host,
		})
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial orderer: %w", err)
	}
	o := &Orderer{
		conn:   conn,
		client: orderer.NewAtomicBroadcastClient(conn),
	}
	o.ctx, o.cancel = context.WithCancel(context.Background())
	o.stream, err = o.client.Broadcast(o.ctx)
	if err != nil {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("connection to orderer: %w", err)
	}
	return o, nil
}

// Broadcast sends a signed envelope with an endorsed EndorserTransaction for ordering.
func (o Orderer) Broadcast(env *common.Envelope) error {
	if err := o.stream.Send(env); err != nil {
		return err
	}
	resp, err := o.stream.Recv()
	if err != nil {
		return err
	}
	if resp.Status != common.Status_SUCCESS {
		return fmt.Errorf("orderer rejected: %s", resp.Status.String())
	}
	return nil
}

func (o *Orderer) Close() error {
	if err := o.stream.CloseSend(); err != nil {
		o.cancel()
		_ = o.conn.Close()
		return err
	}
	o.cancel()
	return o.conn.Close()
}
