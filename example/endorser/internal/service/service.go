/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-lib-go/common/flogging"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-committer/utils/connection"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	efab "github.com/hyperledger/fabric-x-sdk/endorsement/fabric"
	efabx "github.com/hyperledger/fabric-x-sdk/endorsement/fabricx"
	"github.com/hyperledger/fabric-x-sdk/example/endorser/internal/config"
	"github.com/hyperledger/fabric-x-sdk/identity"
	"github.com/hyperledger/fabric-x-sdk/network"
	nfab "github.com/hyperledger/fabric-x-sdk/network/fabric"
	nfabx "github.com/hyperledger/fabric-x-sdk/network/fabricx"
	"github.com/hyperledger/fabric-x-sdk/state"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthgrpc "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// Service implements the Endorser gRPC API
type Service struct {
	peer.EndorserServer
	healthcheck  *health.Server
	channel      string
	namespace    string
	synchronizer *network.Synchronizer
	executor     Executor
	builder      endorsement.Builder
	writeDB      *state.VersionedDB
	readDB       *state.VersionedDB
	logger       sdk.Logger
}

type Executor interface {
	// Execute takes a chaincode-style invocation and returns a read/write set, status, payload and optional event.
	Execute(context.Context, endorsement.Invocation) (endorsement.ExecutionResult, error)
}

// New creates a new Service instance, loading the Fabric MSP identity from cfg.Identity.
func New(cfg config.Config) (*Service, error) {
	signer, err := identity.SignerFromMSP(cfg.Identity.MSPDir, cfg.Identity.MspID)
	if err != nil {
		return nil, fmt.Errorf("failed to load signer: %w", err)
	}
	return NewWithSigner(cfg, signer)
}

// NewWithSigner creates a new Service with an already-constructed signer,
// bypassing MSP loading. Useful in tests or when the signer is provided externally.
func NewWithSigner(cfg config.Config, signer sdk.Signer) (*Service, error) {
	logger := flogging.MustGetLogger("endorser")

	// initialize sqlite databases
	writeDB, err := state.NewWriteDB(cfg.ChannelID, cfg.Database.ConnStr)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	readDB, err := state.NewReadDB(cfg.ChannelID, cfg.Database.ConnStr)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}

	var executor SampleExecutor
	var builder endorsement.Builder
	var sync *network.Synchronizer
	switch cfg.Protocol {
	case "fabric":
		executor = SampleExecutor{db: readDB}
		builder = efab.NewEndorsementBuilder(signer)
		sync, err = nfab.NewSynchronizer(readDB, cfg.ChannelID, ToPeerConf(cfg.Committer), signer, logger, writeDB)
	case "fabric-x", "":
		executor = SampleExecutor{db: readDB, monotonicVersions: true}
		builder = efabx.NewEndorsementBuilder(signer)
		sync, err = nfabx.NewSynchronizer(readDB, cfg.ChannelID, ToPeerConf(cfg.Committer), signer, logger, writeDB)
	}
	if err != nil {
		return nil, fmt.Errorf("create synchronizer: %w", err)
	}

	s := &Service{
		healthcheck:  connection.DefaultHealthCheckService(),
		channel:      cfg.ChannelID,
		namespace:    cfg.Namespace,
		synchronizer: sync,
		executor:     executor,
		builder:      builder,
		readDB:       readDB,
		writeDB:      writeDB,
		logger:       logger,
	}

	logger.Info("endorser initialized")
	return s, nil
}

// ReadDB returns the write-side database used to store committed blocks.
// Exposed for integration tests.
func (s *Service) BlockNumber(ctx context.Context) (uint64, error) {
	return s.readDB.BlockNumber(ctx)
}

// RegisterService implements connection.Service interface
// This registers the gRPC service handlers with the gRPC server
func (s *Service) RegisterService(server *grpc.Server) {
	peer.RegisterEndorserServer(server, s)
	healthgrpc.RegisterHealthServer(server, s.healthcheck)
	reflection.Register(server)
	s.logger.Infof("service handlers registered")
}

// Run implements connection.Service interface
// This runs background tasks concurrently with the gRPC server
func (s *Service) Run(ctx context.Context) error {
	go s.synchronizer.Start(ctx)
	s.logger.Infof("synchronization started")

	// Wait for context cancellation (shutdown signal)
	<-ctx.Done()
	s.logger.Infof("stopping endorser")

	// Cleanup
	s.writeDB.Close()
	s.readDB.Close()

	s.logger.Infof("closed synchronization service and database")
	return nil
}

// ProcessPropasal is the API for incoming requests.
func (s *Service) ProcessProposal(ctx context.Context, prop *peer.SignedProposal) (*peer.ProposalResponse, error) {
	// parse and validate
	inv, err := endorsement.Parse(prop, time.Now())
	if err != nil {
		s.logger.Infof("tx=%s err=%s", inv.TxID, err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if inv.Channel != s.channel {
		s.logger.Infof("tx=%s err=wrong channel: %s", inv.TxID, inv.Channel)
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("channel must be %s", s.channel))
	}
	// We enforce a single namespace.
	if inv.CCID.Name != s.namespace {
		s.logger.Infof("tx=%s err=wrong namespace: %s", inv.TxID, inv.CCID.Name)
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("namespace must be %s", s.namespace))
	}
	// the `peer chaincode invoke` command leaves the version empty, causing an INVALID_CHAINCODE at commit time.
	// since we don't really use the version anyway, we just default to 1.0.
	if inv.CCID.Version == "" {
		inv.CCID.Version = "1.0"
	}

	// execute the transaction
	res, err := s.executor.Execute(ctx, inv)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// sign and respond
	end, err := s.builder.Endorse(inv, res)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("endorsement: %s", err.Error()))
	}

	// log
	var pl string
	if len(end.Response.Payload) < 512 {
		pl = string(end.Response.Payload)
	} else {
		pl = fmt.Sprintf("(%db)", len(end.Response.Payload))
	}
	s.logger.Infof("tx=%s st=%d ns=%s fn=%s args=%d res=%s", inv.TxID, end.Response.Status, inv.CCID.Name, string(inv.Args[0]), len(inv.Args)-1, pl)

	return end, nil
}

// WaitForReady implements connection.Service interface.
// The endorser waits until its blockheight is equal to the committing peer it's connected to.
func (s *Service) WaitForReady(ctx context.Context) bool {
	for {
		if err := s.synchronizer.Ready(); err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ToPeerConf converts a ClientConfig to SDK's PeerConf.
// This conversion layer keeps the config package independent from SDK internals.
func ToPeerConf(cfg config.ClientConfig) network.PeerConf {
	return network.PeerConf{
		Address: cfg.Endpoint.Address(),
		TLS: network.TLSConfig{
			Mode:        cfg.TLS.Mode,
			CertPath:    cfg.TLS.CertPath,
			KeyPath:     cfg.TLS.KeyPath,
			CACertPaths: cfg.TLS.CACertPaths,
		},
	}
}
