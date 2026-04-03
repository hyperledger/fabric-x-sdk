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
	logger   sdk.Logger
	// waitAfterSubmit is a temporary patch to wait for finality, until we have a proper finality listener. FIXME
	waitAfterSubmit time.Duration
}

// OrdererConf tells the submitter how to reach an orderer.
type OrdererConf struct {
	Address        string // Address and port
	TLSPath        string // CA cert path for server TLS verification (optional)
	ClientCertPath string // client cert path for mTLS (optional)
	ClientKeyPath  string // client key path for mTLS (optional)
}

func NewSubmitter(config []OrdererConf, packager TxPackager, waitAfterSubmit time.Duration, logger sdk.Logger) (*FabricSubmitter, error) {
	if len(config) == 0 {
		return nil, errors.New("no orderers configured")
	}

	or := make([]*Orderer, len(config))
	for i, o := range config {
		var caPem, clientCert, clientKey []byte
		var err error
		if len(o.TLSPath) > 0 {
			caPem, err = os.ReadFile(o.TLSPath)
			if err != nil {
				return nil, err
			}
		}
		if len(o.ClientCertPath) > 0 {
			clientCert, err = os.ReadFile(o.ClientCertPath)
			if err != nil {
				return nil, err
			}
			clientKey, err = os.ReadFile(o.ClientKeyPath)
			if err != nil {
				return nil, err
			}
		}
		or[i], err = NewOrderer(o.Address, caPem, clientCert, clientKey)
		if err != nil {
			return nil, err
		}
	}

	return &FabricSubmitter{
		orderers:        or,
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

// NewOrderer dials an orderer with optional TLS (caPem is the server CA cert) or mTLS.
func NewOrderer(addr string, caPem, clientCert, clientKey []byte) (*Orderer, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("orderer address [%s] must contain port: %w", addr, err)
	}
	creds := insecure.NewCredentials()
	if len(caPem) > 0 {
		roots := x509.NewCertPool()
		if ok := roots.AppendCertsFromPEM(caPem); !ok {
			return nil, fmt.Errorf("failed to append orderer TLS cert")
		}
		tlsCfg := &tls.Config{RootCAs: roots, ServerName: host}
		if len(clientCert) > 0 {
			cert, err := tls.X509KeyPair(clientCert, clientKey)
			if err != nil {
				return nil, fmt.Errorf("orderer mTLS key pair: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		creds = credentials.NewTLS(tlsCfg)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial orderer: %w", err)
	}
	return &Orderer{
		conn:   conn,
		client: orderer.NewAtomicBroadcastClient(conn),
		addr:   addr,
	}, nil
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
