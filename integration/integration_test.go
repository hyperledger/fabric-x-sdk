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
	"path"
	"sync"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
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

// TestFablo runs the full test corpus against a real two-org Fabric network
// started with fablo. It is skipped automatically in short mode.
func TestFablo(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping fablo tests in short mode")
	}
	runAll(t, newFabloSetup(t))
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
			s := newWithTestBackend(t, nt, fabrictest.Config{
				BlockCutTime:  time.Second,
				MaxTxPerBlock: 2,
			})
			testTwoTransactions(t, s)
		})
	}
}

// --- test setup ---

// captureHandler records every parsed block so tests can inspect transactions.
type captureHandler struct {
	mu     sync.Mutex
	blocks []blocks.Block
}

func (h *captureHandler) Handle(_ context.Context, b blocks.Block) error {
	h.mu.Lock()
	h.blocks = append(h.blocks, b)
	h.mu.Unlock()
	return nil
}

// findTx returns the captured transaction with the given ID, or nil if not found.
// Call this only after finality is established (e.g. waitForKeyValue returned).
func (h *captureHandler) findTx(txID string) *blocks.Transaction {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.blocks {
		for j := range h.blocks[i].Transactions {
			if h.blocks[i].Transactions[j].ID == txID {
				tx := h.blocks[i].Transactions[j]
				return &tx
			}
		}
	}
	return nil
}

type testSetup struct {
	channel           string
	namespace         string
	networkType       string // "fabric" or "fabric-x"
	localDB           *state.VersionedDB
	monotonicVersions bool
	signer            sdk.Signer
	builders          []endorsement.Builder
	submitter         *network.FabricSubmitter
	capture           *captureHandler
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
func newWithTestBackend(t *testing.T, networkType string, batching ...fabrictest.Config) *testSetup {
	t.Helper()
	var batchCfg fabrictest.Config
	if len(batching) > 0 {
		batchCfg = batching[0]
	}
	nw, err := fabrictest.Start(t.Context(), "basic", networkType, batchCfg)
	if err != nil {
		t.Fatalf("fabrictest.Start: %v", err)
	}

	cfg := config{
		NetworkType: networkType,
		Channel:     "mychannel",
		Namespace:   "basic",
		Peer: network.PeerConf{
			Address: fmt.Sprintf("127.0.0.1:%d", nw.PeerPort),
			TLS:     network.TLSConfig{Mode: network.TLSModeNone},
		},
		Orderers: []network.OrdererConf{{
			Address: fmt.Sprintf("127.0.0.1:%d", nw.OrdererPort),
			TLS:     network.TLSConfig{Mode: network.TLSModeNone},
		}},
	}

	return newSetup(t, networkType, cfg)
}

// newFabloSetup returns a testSetup pointed at a running two-org Fablo network.
func newFabloSetup(t *testing.T) *testSetup {
	t.Helper()
	cryptoBase := path.Join("..", "testdata", "fablo", "fablo-target", "fabric-config",
		"crypto-config", "peerOrganizations")
	peer0 := path.Join(cryptoBase, "org1.example.com", "peers", "peer0.org1.example.com")
	orderer0 := path.Join(cryptoBase, "orderer.example.com", "peers", "orderer0.group1.orderer.example.com")
	user := path.Join(cryptoBase, "org1.example.com", "users", "User1@org1.example.com")

	cfg := config{
		NetworkType: "fabric",
		Channel:     "mychannel",
		Namespace:   "basic",
		Orderers: []network.OrdererConf{{
			Address: "127.0.0.1:7030",
			TLS: network.TLSConfig{
				Mode:        network.TLSModeTLS,
				ServerName:  "orderer0.group1.orderer.example.com",
				CACertPaths: []string{path.Join(orderer0, "tls", "ca.crt")},
			},
		}},
		Peer: network.PeerConf{
			Address: "127.0.0.1:7041",
			TLS: network.TLSConfig{
				Mode:        network.TLSModeTLS,
				ServerName:  "peer0.org1.example.com",
				CACertPaths: []string{path.Join(peer0, "tls", "ca.crt")},
			},
		},
		SignerMSPDir: path.Join(user, "msp"),
		SignerMSPID:  "Org1MSP",
	}

	conn, err := net.DialTimeout("tcp", cfg.Peer.Address, time.Second)
	if err != nil {
		t.Fatalf("fablo network not running (start with make start-fablo)")
	}
	conn.Close()

	s := newSetup(t, "fabric", cfg)

	// The fablo network enforces AND('Org1MSP.member', 'Org2MSP.member'), so
	// we need an endorser for each org.
	org2User := path.Join(cryptoBase, "org2.example.com", "users", "User1@org2.example.com")
	org2Signer, err := identity.SignerFromMSP(path.Join(org2User, "msp"), "Org2MSP")
	if err != nil {
		t.Fatalf("SignerFromMSP org2: %v", err)
	}
	s.builders = append(s.builders, efab.NewEndorsementBuilder(org2Signer))

	return s
}

// newTestCommitterSetup returns a testSetup pointed at a running Fabric-X test committer.
func newTestCommitterSetup(t *testing.T) *testSetup {
	t.Helper()
	cryptoBase := path.Join("..", "testdata", "crypto", "peerOrganizations", "Org1")
	committer := path.Join(cryptoBase, "peers", "committer.org1.example.com")
	user := path.Join(cryptoBase, "users", "User1@org1.example.com")

	cfg := config{
		Channel:   "mychannel",
		Namespace: "basic",
		Orderers: []network.OrdererConf{{
			Address: "127.0.0.1:7050",
			TLS: network.TLSConfig{
				Mode:        network.TLSModeMTLS,
				CertPath:    path.Join(user, "tls", "client.crt"),
				KeyPath:     path.Join(user, "tls", "client.key"),
				CACertPaths: []string{path.Join(committer, "tls", "ca.crt")},
			},
		}},
		Peer: network.PeerConf{
			Address: "127.0.0.1:4001",
			TLS: network.TLSConfig{
				Mode:        network.TLSModeMTLS,
				CertPath:    path.Join(user, "tls", "client.crt"),
				KeyPath:     path.Join(user, "tls", "client.key"),
				CACertPaths: []string{path.Join(committer, "tls", "ca.crt")},
			},
		},
		SignerMSPDir: path.Join(user, "msp"),
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

	capture := &captureHandler{}

	var builder endorsement.Builder
	var submitter *network.FabricSubmitter
	var sync *network.Synchronizer
	switch networkType {
	case "fabric":
		builder = efab.NewEndorsementBuilder(signer)
		sync, err = nfab.NewSynchronizer(localDB, cfg.Channel, cfg.Peer, signer, log, localDB, capture)
		if err != nil {
			t.Fatalf("NewSynchronizaer: %v", err)
		}
		submitter, err = nfab.NewSubmitter(cfg.Orderers, signer, 0, log)
	case "fabric-x":
		builder = efabx.NewEndorsementBuilder(signer)
		sync, err = nfabx.NewSynchronizer(localDB, cfg.Channel, cfg.Peer, signer, log, localDB, capture)
		if err != nil {
			t.Fatalf("NewSynchronizaer: %v", err)
		}
		submitter, err = nfabx.NewSubmitter(cfg.Orderers, signer, 0, log)
	}
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}

	go sync.Start(t.Context())              //nolint:errcheck
	t.Cleanup(func() { submitter.Close() }) //nolint:errcheck

	waitUntilSynced(t, sync, 10*time.Second)

	return &testSetup{
		channel:           cfg.Channel,
		namespace:         cfg.Namespace,
		localDB:           localDB,
		monotonicVersions: monotonicVersions,
		networkType:       networkType,
		signer:            signer,
		builders:          []endorsement.Builder{builder},
		submitter:         submitter,
		capture:           capture,
	}
}

func waitUntilSynced(t *testing.T, sync *network.Synchronizer, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()

	for {
		if err := sync.Ready(); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("timeout waiting for sync")
		case <-time.After(100 * time.Millisecond):
		}
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
	result := endorsement.Success(rws, nil, nil)
	var responses []*peer.ProposalResponse
	for _, b := range s.builders {
		resp, err := b.Endorse(inv, result)
		if err != nil {
			return fmt.Errorf("Endorse: %w", err)
		}
		responses = append(responses, resp)
	}
	end := sdk.Endorsement{Proposal: inv.Proposal, Responses: responses}
	return s.submitter.Submit(ctx, end)
}

// endorse builds a proposal, applies endr's result function with every builder,
// and returns the combined sdk.Endorsement (one response per builder).
func (s *testSetup) endorse(t *testing.T, endr *localEndorser, args [][]byte) sdk.Endorsement {
	t.Helper()
	prop, err := network.NewSignedProposal(s.signer, s.channel, s.namespace, "1.0", args)
	if err != nil {
		t.Fatalf("NewSignedProposal: %v", err)
	}
	inv, err := endorsement.Parse(prop, time.Time{})
	if err != nil {
		t.Fatalf("endorsement.Parse: %v", err)
	}
	result := endr.result(inv)
	var responses []*peer.ProposalResponse
	for _, b := range s.builders {
		resp, err := b.Endorse(inv, result)
		if err != nil {
			t.Fatalf("Endorse: %v", err)
		}
		responses = append(responses, resp)
	}
	proposal, err := protoutil.UnmarshalProposal(prop.ProposalBytes)
	if err != nil {
		t.Fatalf("UnmarshalProposal: %v", err)
	}
	return sdk.Endorsement{Proposal: proposal, Responses: responses}
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

// localEndorser holds a result function that is applied to every builder in
// testSetup.builders. It lets tests inject success or error outcomes without a
// gRPC hop.
type localEndorser struct {
	result func(inv endorsement.Invocation) endorsement.ExecutionResult
}
