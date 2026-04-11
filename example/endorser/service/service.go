/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-committer/utils/connection"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	efab "github.com/hyperledger/fabric-x-sdk/endorsement/fabric"
	efabx "github.com/hyperledger/fabric-x-sdk/endorsement/fabricx"
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
	healthcheck       *health.Server
	channel           string
	synchronizer      *network.Synchronizer
	executors         map[string]Executor
	builder           endorsement.Builder
	writeDB           *state.VersionedDB
	readDB            *state.VersionedDB
	monotonicVersions bool
	logger            sdk.Logger
}

type Executor interface {
	// Execute takes a chaincode-style invocation and a StoreFactory, and returns a read/write set, status, payload and optional event.
	Execute(context.Context, StoreFactory, endorsement.Invocation) (endorsement.ExecutionResult, error)
}

// StateStore reads from the world state and captures reads and writes.
// At the end of execution, call Result() to get the read/write set for this transaction.
type StateStore interface {
	GetState(key string) ([]byte, error)
	PutState(key string, value []byte) error
	DelState(key string) error
	AddLog(address []byte, topics [][]byte, data []byte)
	Result() blocks.ReadWriteSet
}

// StoreFactory creates a SimulationStore at the given block height.
// Pass 0 for current state.
type StoreFactory func(blockNum uint64) (StateStore, error)

// ServiceConfig is the minimal configuration needed to construct a Service.
// It contains only what the service itself uses — callers map their own
// application config to this struct.
type ServiceConfig struct {
	ChannelID string
	Protocol  string // "fabric" or "fabric-x" (default)
	Committer network.PeerConf
	DBConnStr string
}

// New creates a new Service instance, loading the Fabric MSP identity from mspDir and mspID.
func New(cfg ServiceConfig, mspDir, mspID string, executors map[string]Executor, logger sdk.Logger) (*Service, error) {
	signer, err := identity.SignerFromMSP(mspDir, mspID)
	if err != nil {
		return nil, fmt.Errorf("failed to load signer: %w", err)
	}
	return NewWithSigner(cfg, signer, executors, logger)
}

// NewWithSigner creates a new Service with an already-constructed signer,
// bypassing MSP loading. Useful in tests or when the signer is provided externally.
func NewWithSigner(cfg ServiceConfig, signer sdk.Signer, executors map[string]Executor, logger sdk.Logger) (*Service, error) {

	// initialize sqlite databases
	writeDB, err := state.NewWriteDB(cfg.ChannelID, cfg.DBConnStr)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	readDB, err := state.NewReadDB(cfg.ChannelID, cfg.DBConnStr)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}

	var builder endorsement.Builder
	var sync *network.Synchronizer
	var monotonicVersions bool
	switch cfg.Protocol {
	case "fabric":
		builder = efab.NewEndorsementBuilder(signer)
		sync, err = nfab.NewSynchronizer(readDB, cfg.ChannelID, cfg.Committer, signer, logger, writeDB)
	case "fabric-x", "":
		monotonicVersions = true
		builder = efabx.NewEndorsementBuilder(signer)
		sync, err = nfabx.NewSynchronizer(readDB, cfg.ChannelID, cfg.Committer, signer, logger, writeDB)
	}
	if err != nil {
		return nil, fmt.Errorf("create synchronizer: %w", err)
	}

	s := &Service{
		healthcheck:       connection.DefaultHealthCheckService(),
		channel:           cfg.ChannelID,
		synchronizer:      sync,
		executors:         executors,
		builder:           builder,
		readDB:            readDB,
		writeDB:           writeDB,
		monotonicVersions: monotonicVersions,
		logger:            logger,
	}

	logger.Infof("endorser initialized")
	return s, nil
}

// BlockNumber is used for integration tests.
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
// This runs background tasks concurrently with the gRPC server.
// In this case, the synchronizer which makes sure our world state
// database is up to date with the committing peer we connect to.
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
	// Look up the executor for this namespace.
	executor, ok := s.executors[inv.CCID.Name]
	if !ok {
		s.logger.Infof("tx=%s err=unknown namespace: %s", inv.TxID, inv.CCID.Name)
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("unknown namespace: %s", inv.CCID.Name))
	}
	// the `peer chaincode invoke` command leaves the version empty, causing an INVALID_CHAINCODE at commit time.
	// since we don't really use the version anyway, we just default to 1.0.
	if inv.CCID.Version == "" {
		inv.CCID.Version = "1.0"
	}

	// build a factory that closes over the per-request context and namespace
	newStore := func(blockNum uint64) (StateStore, error) {
		return state.NewSimulationStore(ctx, s.readDB, inv.CCID.Name, blockNum, s.monotonicVersions)
	}

	// execute the transaction
	res, err := executor.Execute(ctx, newStore, inv)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	// create the response
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
