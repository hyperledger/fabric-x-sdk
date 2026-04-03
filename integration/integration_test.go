/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package integration provides tests that exercise a large surface of the SDK.
// Tests are backend-agnostic functions registered in cases_test.go.
// Each top-level Test* function selects a backend, initialises it once, and
// runs all cases against it via runAll.
package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	bfab "github.com/hyperledger/fabric-x-sdk/blocks/fabric"
	bfabx "github.com/hyperledger/fabric-x-sdk/blocks/fabricx"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	efab "github.com/hyperledger/fabric-x-sdk/endorsement/fabric"
	efabx "github.com/hyperledger/fabric-x-sdk/endorsement/fabricx"
	"github.com/hyperledger/fabric-x-sdk/fabrictest"
	"github.com/hyperledger/fabric-x-sdk/identity"
	"github.com/hyperledger/fabric-x-sdk/network"
	nfab "github.com/hyperledger/fabric-x-sdk/network/fabric"
	nfabx "github.com/hyperledger/fabric-x-sdk/network/fabricx"
	"github.com/hyperledger/fabric-x-sdk/state"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/grpc/grpclog"
	_ "modernc.org/sqlite"
)

// --- backends ---

func TestFabric(t *testing.T) {
	runAll(t, newWithTestBackend(t, "fabric"))
}

func TestFabricX(t *testing.T) {
	runAll(t, newWithTestBackend(t, "fabric-x"))
}

// TestFabricXCommitter runs the full test corpus against a real Fabric-X
// committer. It is skipped automatically in short mode.
func TestFabricXCommitter(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping fabric-x committer tests in short mode")
	}

	runAll(t, newTestCommitterSetup(t))
}

// TestFabricTestTwoTransactions verifies that two transactions land in the same block
// when MaxTxPerBlock > 1.
func TestFabricTestTwoTransactions(t *testing.T) {
	for _, nt := range []string{"fabric", "fabric-x"} {
		t.Run(nt, func(t *testing.T) {
			s := newWithTestBackend(t, nt, fabrictest.BatchingConfig{
				BlockCutTime:  time.Second,
				MaxTxPerBlock: 2,
			})
			testTwoTransactions(t, s)
		})
	}
}

// --- test setup ---

type testSetup struct {
	channel           string
	namespace         string
	networkType       string // "fabric" or "fabric-x"
	localDB           *state.VersionedDB
	monotonicVersions bool
	signer            sdk.Signer
	builder           endorsement.Builder
	submitter         *network.FabricSubmitter
}

type config struct {
	NetworkType  string
	Channel      string
	Namespace    string
	Peer         network.PeerConf
	Orderers     []network.OrdererConf
	SignerMSPDir string
	SignerMSPID  string
}

// newWithTestBackend returns a test setup with an in-process fake fabric from the `fabrictest` package.
func newWithTestBackend(t *testing.T, networkType string, batching ...fabrictest.BatchingConfig) *testSetup {
	t.Helper()
	var batchCfg fabrictest.BatchingConfig
	if len(batching) > 0 {
		batchCfg = batching[0]
	}
	nw, err := fabrictest.Start("basic", networkType, batchCfg)
	if err != nil {
		t.Fatalf("fabrictest.Start: %v", err)
	}
	t.Cleanup(nw.Stop)

	cfg := config{
		NetworkType: networkType,
		Channel:     "mychannel",
		Namespace:   "basic",
		Peer:        network.PeerConf{Address: nw.PeerAddr},
		Orderers:    []network.OrdererConf{{Address: nw.OrdererAddr}},
	}

	return newSetup(t, networkType, cfg)
}

// newTestCommitterSetup returns a testSetup pointed at a running Fabric-X committer.
// It calls t.Skip if the committer is not reachable (start with: make start-x).
func newTestCommitterSetup(t *testing.T) *testSetup {
	t.Helper()
	cfg := config{
		Channel:   "mychannel",
		Namespace: "basic",
		Orderers: []network.OrdererConf{{
			Address:        "127.0.0.1:7050",
			TLSPath:        "../testdata/crypto/peerOrganizations/Org1/peers/committer.org1.example.com/tls/ca.crt",
			ClientCertPath: "../testdata/crypto/peerOrganizations/Org1/peers/committer.org1.example.com/tls/server.crt",
			ClientKeyPath:  "../testdata/crypto/peerOrganizations/Org1/peers/committer.org1.example.com/tls/server.key",
		}},
		Peer: network.PeerConf{
			Address:        "127.0.0.1:4001",
			TLSPath:        "../testdata/crypto/peerOrganizations/Org1/peers/committer.org1.example.com/tls/ca.crt",
			ClientCertPath: "../testdata/crypto/peerOrganizations/Org1/peers/committer.org1.example.com/tls/server.crt",
			ClientKeyPath:  "../testdata/crypto/peerOrganizations/Org1/peers/committer.org1.example.com/tls/server.key",
		},
		SignerMSPDir: "../testdata/crypto/peerOrganizations/Org1/users/channel_admin@org1.example.com/msp",
		SignerMSPID:  "Org1MSP",
	}

	conn, err := net.DialTimeout("tcp", cfg.Peer.Address, time.Second)
	if err != nil {
		t.Fatalf("fabric-x committer not running (start with make start-x)")
	}
	conn.Close()

	return newSetup(t, "fabric-x", cfg)
}

func newSetup(t *testing.T, networkType string, cfg config) *testSetup {
	t.Helper()
	log := sdk.NewTestLogger(t, networkType)
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(io.Discard, os.Stderr, os.Stderr)) // silence GRPC logging

	var signer sdk.Signer
	var err error
	if len(cfg.SignerMSPDir) > 0 {
		signer, err = identity.SignerFromMSP(cfg.SignerMSPDir, cfg.SignerMSPID)
		if err != nil {
			t.Fatalf("SignerFromMSP: %v", err)
		}
	} else {
		signer = testSigner{}
	}

	monotonicVersions := networkType == "fabric-x"
	localDB, err := state.NewWriteDB(cfg.Channel, fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
	if err != nil {
		t.Fatalf("state.NewSqlite: %v", err)
	}
	t.Cleanup(func() { localDB.Close() }) //nolint:errcheck

	var parser blocks.BlockParser
	var builder endorsement.Builder
	var submitter *network.FabricSubmitter
	switch networkType {
	case "fabric":
		parser = bfab.NewBlockParser(log)
		builder = efab.NewEndorsementBuilder(signer)
		submitter, err = nfab.NewSubmitter(cfg.Orderers, signer, 0, log)
	case "fabric-x":
		parser = bfabx.NewBlockParser(log)
		builder = efabx.NewEndorsementBuilder(signer)
		submitter, err = nfabx.NewSubmitter(cfg.Orderers, signer, 0, log)
	}
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	t.Cleanup(func() { submitter.Close() }) //nolint:errcheck

	// Synchronizer processes and stores blocks.
	processor := blocks.NewProcessor(parser, []blocks.BlockHandler{localDB})
	sync, err := network.NewSynchronizer(localDB, cfg.Channel, cfg.Peer, signer, processor, log)
	if err != nil {
		t.Fatalf("NewSynchronizer: %v", err)
	}

	go sync.Start(t.Context())         //nolint:errcheck
	t.Cleanup(func() { sync.Close() }) //nolint:errcheck

	return &testSetup{
		channel:           cfg.Channel,
		namespace:         cfg.Namespace,
		localDB:           localDB,
		monotonicVersions: monotonicVersions,
		networkType:       networkType,
		signer:            signer,
		builder:           builder,
		submitter:         submitter,
	}
}

func (s *testSetup) endorseAndSubmit(ctx context.Context, rws blocks.ReadWriteSet) error {
	signedProp, err := network.NewSignedProposal(s.signer, s.channel, s.namespace, "1.0", [][]byte{[]byte("invoke")})
	if err != nil {
		return fmt.Errorf("NewSignedProposal: %w", err)
	}
	inv, err := endorsement.Parse(signedProp, time.Time{})
	if err != nil {
		return fmt.Errorf("endorsement.Parse: %w", err)
	}
	resp, err := s.builder.Endorse(inv, endorsement.Success(rws, nil, nil))
	if err != nil {
		return fmt.Errorf("Endorse: %w", err)
	}
	end := sdk.Endorsement{Proposal: inv.Proposal, Responses: []*peer.ProposalResponse{resp}}
	return s.submitter.Submit(ctx, end)
}

// endorse builds a proposal, sends it to endr, and returns the resulting sdk.Endorsement.
func (s *testSetup) endorse(t *testing.T, endr *localEndorser, args [][]byte) sdk.Endorsement {
	t.Helper()
	prop, err := network.NewSignedProposal(s.signer, s.channel, s.namespace, "1.0", args)
	if err != nil {
		t.Fatalf("NewSignedProposal: %v", err)
	}
	resp, err := endr.ProcessProposal(t.Context(), prop)
	if err != nil {
		t.Fatalf("ProcessProposal: %v", err)
	}
	proposal, err := protoutil.UnmarshalProposal(prop.ProposalBytes)
	if err != nil {
		t.Fatalf("UnmarshalProposal: %v", err)
	}
	return sdk.Endorsement{Proposal: proposal, Responses: []*peer.ProposalResponse{resp}}
}

// waitForKeyValue polls until the key has the given value in the local DB.
func (s *testSetup) waitForKeyValue(t *testing.T, key, value string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, err := s.localDB.GetCurrent(s.namespace, key)
		if err == nil && rec != nil && string(rec.Value) == value {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for key %q = %q", key, value)
}

// waitForBlock polls until the local DB has processed at least minBlock.
func (s *testSetup) waitForBlock(t *testing.T, minBlock uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := s.localDB.BlockNumber(t.Context())
		if n >= minBlock {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for block %d in local DB", minBlock)
}

// sentinel submits a cheap write to sentinelKey and waits for it to appear in
// the local DB. Because transactions are ordered, all previously submitted
// transactions are guaranteed to have been processed when this returns.
func (s *testSetup) sentinel(t *testing.T, sentinelKey string) {
	t.Helper()
	if err := s.endorseAndSubmit(t.Context(), blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: sentinelKey, Value: []byte("done")}},
	}); err != nil {
		t.Fatalf("submit sentinel: %v", err)
	}
	s.waitForKeyValue(t, sentinelKey, "done")
}

// simulate creates a SimulationStore at blockNum, reads key, and returns the value and RWS.
// Pass blockNum=0 to use the current block height.
func (s *testSetup) simulate(t *testing.T, blockNum uint64, key string) ([]byte, blocks.ReadWriteSet) {
	t.Helper()
	sim, err := state.NewSimulationStore(t.Context(), s.localDB, s.namespace, blockNum, s.monotonicVersions)
	if err != nil {
		t.Fatalf("NewSimulationStore: %v", err)
	}
	val, err := sim.GetState(key)
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	return val, sim.Result()
}

// newSimStore creates a SimulationStore at blockNum. Pass 0 to use the current block height.
func (s *testSetup) newSimStore(t *testing.T, blockNum uint64) *state.SimulationStore {
	t.Helper()
	sim, err := state.NewSimulationStore(t.Context(), s.localDB, s.namespace, blockNum, s.monotonicVersions)
	if err != nil {
		t.Fatalf("NewSimulationStore: %v", err)
	}
	return sim
}

// --- test signer ---

// testSigner is a minimal sdk.Signer that returns fixed bytes.
// fabrictest does not verify signatures, so this is sufficient for integration tests.
type testSigner struct{}

func (testSigner) Sign(_ []byte) ([]byte, error) { return []byte("sig"), nil }
func (testSigner) Serialize() ([]byte, error)    { return []byte("identity"), nil }

// --- local endorser ---

// localEndorser endorses proposals in-process without a gRPC hop.
// result is called with the parsed invocation and its return value is used as
// the execution result, allowing tests to inject success or error outcomes.
type localEndorser struct {
	builder endorsement.Builder
	result  func(inv endorsement.Invocation) endorsement.ExecutionResult
}

func (e *localEndorser) ProcessProposal(_ context.Context, prop *peer.SignedProposal) (*peer.ProposalResponse, error) {
	inv, err := endorsement.Parse(prop, time.Time{})
	if err != nil {
		return nil, err
	}
	return e.builder.Endorse(inv, e.result(inv))
}
