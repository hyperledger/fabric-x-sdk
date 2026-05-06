/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package service_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"testing"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric-x-sdk/example/endorser/config"
	"github.com/hyperledger/fabric-x-sdk/example/endorser/service"
	"github.com/hyperledger/fabric-x-sdk/fabrictest"
	"github.com/hyperledger/fabric-x-sdk/identity"
	"github.com/hyperledger/fabric-x-sdk/network"
	nfab "github.com/hyperledger/fabric-x-sdk/network/fabric"
	nfabx "github.com/hyperledger/fabric-x-sdk/network/fabricx"
	"google.golang.org/grpc"
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

// --- test setup ---

// endorserSetup holds the live components for one test scenario.
type endorserSetup struct {
	svc          *service.Service
	ec           *network.EndorsementClient
	endorserAddr string
	namespace    string
	submitter    *network.FabricSubmitter
	networkType  string
	ordererAddr  string
}

// newWithTestBackend returns a test setup with an in-process fake fabric from the `fabrictest` package.
func newWithTestBackend(t *testing.T, networkType string) *endorserSetup {
	t.Helper()

	fnet, err := fabrictest.Start(t.Context(), "basic", networkType, fabrictest.Config{})
	if err != nil {
		t.Fatalf("fabrictest.Start: %v", err)
	}

	cfg := config.Config{
		ChannelID: "mychannel",
		Protocol:  networkType,
		Database:  config.DatabaseConfig{ConnStr: fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())},
		Committer: config.ClientConfig{
			Endpoint: &config.Endpoint{Host: "127.0.0.1", Port: fnet.PeerPort},
		},
	}

	return newSetup(t, cfg, testSigner{}, testSigner{}, fmt.Sprintf("127.0.0.1:%d", fnet.OrdererPort), "basic")
}

// newTestCommitterSetup returns a test setup pointed at a running Fabric-X committer.
// It calls t.Skip if the committer is not reachable (start with: make start-x).
func newTestCommitterSetup(t *testing.T) *endorserSetup {
	t.Helper()

	peerAddr := "127.0.0.1:4001"
	ordererAddr := "127.0.0.1:7050"

	// Check if committer is reachable
	conn, err := net.DialTimeout("tcp", peerAddr, time.Second)
	if err != nil {
		t.Fatal("fabric-x committer not running (start with make start-x)")
	}
	conn.Close()

	cfg := config.Config{
		ChannelID: "mychannel",
		Protocol:  "fabric-x",
		Database:  config.DatabaseConfig{ConnStr: fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())},
		Committer: config.ClientConfig{
			Endpoint: &config.Endpoint{Host: "127.0.0.1", Port: 4001},
			TLS: config.TLSConfig{
				Mode:        "mtls",
				CACertPaths: []string{"../../../../testdata/crypto/peerOrganizations/Org1/peers/committer.org1.example.com/tls/ca.crt"},
				CertPath:    "../../../../testdata/crypto/peerOrganizations/Org1/users/User1@org1.example.com/tls/client.crt",
				KeyPath:     "../../../../testdata/crypto/peerOrganizations/Org1/users/User1@org1.example.com/tls/client.key",
			},
		},
	}

	serviceSigner, err := identity.SignerFromMSP(
		"../../../../testdata/crypto/peerOrganizations/Org1/peers/endorser.org1.example.com/msp",
		"Org1MSP",
	)
	if err != nil {
		t.Fatalf("SignerFromMSP (service): %v", err)
	}

	clientSigner, err := identity.SignerFromMSP(
		"../../../../testdata/crypto/peerOrganizations/Org1/users/User1@org1.example.com/msp",
		"Org1MSP",
	)
	if err != nil {
		t.Fatalf("SignerFromMSP (client): %v", err)
	}

	return newSetup(t, cfg, serviceSigner, clientSigner, ordererAddr, "basic")
}

// newSetup creates a new endorserSetup.
// serviceSigner is used by the endorser service (signs endorsements).
// clientSigner is used by the test client (signs proposals and the transaction envelope).
// Using distinct signers reflects the real deployment where the endorser service and
// the submitting client are different parties with different MSP identities.
func newSetup(t *testing.T, cfg config.Config, serviceSigner, clientSigner sdk.Signer, ordererAddr, namespace string) *endorserSetup {
	t.Helper()

	// Create service
	executors := map[string]service.Executor{
		namespace: kvExecutor{},
	}
	svcCfg := service.ServiceConfig{
		ChannelID: cfg.ChannelID,
		Protocol:  cfg.Protocol,
		Committer: cfg.Committer.ToPeerConf(),
		DBConnStr: cfg.Database.ConnStr,
	}
	svc, err := service.NewWithSigner(svcCfg, serviceSigner, executors, sdk.NewTestLogger(t, "endorser"))
	if err != nil {
		t.Fatalf("NewWithSigner: %v", err)
	}

	// Start synchronizer
	syncCtx, syncCancel := context.WithCancel(t.Context())
	t.Cleanup(syncCancel)
	go svc.Run(syncCtx) //nolint:errcheck

	// Start gRPC server
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	grpcSrv := grpc.NewServer()
	svc.RegisterService(grpcSrv)
	go grpcSrv.Serve(lis) //nolint:errcheck
	t.Cleanup(grpcSrv.Stop)

	// Create endorsement client
	ec, err := network.NewEndorsementClient(
		[]network.PeerConf{{
			Address: lis.Addr().String(),
			TLS:     network.TLSConfig{Mode: network.TLSModeNone},
		}},
		clientSigner, cfg.ChannelID, namespace, "1.0",
	)
	if err != nil {
		t.Fatalf("NewEndorsementClient: %v", err)
	}
	t.Cleanup(func() { ec.Close() }) //nolint:errcheck

	// Create submitter
	log := sdk.NewTestLogger(t, "endorser-test")

	// Use the same TLS config for orderer as for committer
	orderers := []network.OrdererConf{{
		Address: ordererAddr,
		TLS:     cfg.Committer.ToPeerConf().TLS,
	}}

	var submitter *network.FabricSubmitter
	switch cfg.Protocol {
	case "fabric":
		submitter, err = nfab.NewSubmitter(orderers, clientSigner, 0, log)
	case "fabric-x":
		submitter, err = nfabx.NewSubmitter(orderers, clientSigner, 0, log)
	}
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	t.Cleanup(func() { submitter.Close() }) //nolint:errcheck

	svc.WaitForReady(t.Context())

	return &endorserSetup{
		svc:          svc,
		ec:           ec,
		namespace:    namespace,
		endorserAddr: lis.Addr().String(),
		submitter:    submitter,
		networkType:  cfg.Protocol,
		ordererAddr:  ordererAddr,
	}
}

// proposeAndSubmit sends args to the endorser via gRPC and submits the resulting endorsement.
func (s *endorserSetup) proposeAndSubmit(t *testing.T, args [][]byte) {
	t.Helper()
	end, err := s.ec.ExecuteTransaction(t.Context(), s.namespace, "1.0", args)
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if err := s.submitter.Submit(t.Context(), end); err != nil {
		t.Fatalf("Submit: %v", err)
	}
}

// waitForBlock polls svc.readDB until the block number is >= minBlock.
func (s *endorserSetup) waitForBlock(t *testing.T, minBlock uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, _ := s.svc.BlockNumber(t.Context())
		if n >= minBlock {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for block %d", minBlock)
}

// --- test executor ---

// kvExecutor is a minimal Executor for testing the service wiring.
// Args: [key, value] to write, [key] to read. Anything else returns BadRequest.
type kvExecutor struct{}

func (kvExecutor) Execute(_ context.Context, newStore service.StoreFactory, inv endorsement.Invocation) (endorsement.ExecutionResult, error) {
	store, err := newStore(0)
	if err != nil {
		return endorsement.ExecutionResult{}, fmt.Errorf("simulation store: %w", err)
	}
	switch len(inv.Args) {
	case 1: // read
		val, err := store.GetState(string(inv.Args[0]))
		if err != nil {
			return endorsement.ExecutionResult{}, fmt.Errorf("get: %w", err)
		}
		return endorsement.Success(store.Result(), nil, val), nil
	case 2: // write
		if err := store.PutState(string(inv.Args[0]), inv.Args[1]); err != nil {
			return endorsement.ExecutionResult{}, fmt.Errorf("put: %w", err)
		}
		return endorsement.Success(store.Result(), nil, inv.Args[1]), nil
	default:
		return endorsement.BadRequest("usage: [key] or [key] [value]"), nil
	}
}

// --- test signer ---

// testSigner is a minimal sdk.Signer returning fixed bytes.
// fabrictest does not verify signatures, so this is sufficient.
type testSigner struct{}

func (testSigner) Sign(_ []byte) ([]byte, error) { return []byte("sig"), nil }
func (testSigner) Serialize() ([]byte, error)    { return []byte("identity"), nil }

// --- test cases ---

type testCase struct {
	name string
	fn   func(*testing.T, *endorserSetup)
}

var cases = []testCase{
	{"SetAndGet", testEndorserSetAndGet},
	{"GetMissingKey", testEndorserGetMissingKey},
	{"BadRequest", testEndorserBadRequest},
	{"WrongChannel", testEndorserWrongChannel},
	{"SetThenOverwrite", testEndorserSetThenOverwrite},
}

// runAll executes every case as a subtest against s.
// The setup is shared; isolation is achieved via unique keys per case.
func runAll(t *testing.T, s *endorserSetup) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, s)
		})
	}
}

// --- individual test cases ---

// testEndorserSetAndGet writes a key and reads it back.
func testEndorserSetAndGet(t *testing.T, s *endorserSetup) {
	key := t.Name() + "/" + rand.Text()

	currentBlock, _ := s.svc.BlockNumber(t.Context())
	s.proposeAndSubmit(t, [][]byte{[]byte(key), []byte("hello")})
	s.waitForBlock(t, currentBlock+1, 5*time.Second)

	end, err := s.ec.ExecuteTransaction(t.Context(), s.namespace, "1.0", [][]byte{[]byte(key)})
	if err != nil {
		t.Fatalf("ExecuteTransaction get: %v", err)
	}
	if len(end.Responses) == 0 {
		t.Fatal("no endorsement responses")
	}
	if got := string(end.Responses[0].Response.Payload); got != "hello" {
		t.Fatalf("expected %q, got %q", "hello", got)
	}
}

// testEndorserGetMissingKey confirms that getting an absent key returns an empty payload without error.
func testEndorserGetMissingKey(t *testing.T, s *endorserSetup) {
	key := t.Name() + "/nonexistent"

	end, err := s.ec.ExecuteTransaction(t.Context(), s.namespace, "1.0", [][]byte{[]byte(key)})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if len(end.Responses) == 0 {
		t.Fatal("no endorsement responses")
	}
	resp := end.Responses[0].Response
	if resp.Status != 200 {
		t.Fatalf("expected status 200, got %d", resp.Status)
	}
	if len(resp.Payload) != 0 {
		t.Fatalf("expected empty payload for missing key, got %q", resp.Payload)
	}
}

// testEndorserBadRequest confirms that too many args returns a 400 status in the response.
func testEndorserBadRequest(t *testing.T, s *endorserSetup) {
	end, err := s.ec.ExecuteTransaction(t.Context(), s.namespace, "1.0", [][]byte{[]byte("a"), []byte("b"), []byte("c")})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if len(end.Responses) == 0 {
		t.Fatal("no endorsement responses")
	}
	if status := end.Responses[0].Response.Status; status != 400 {
		t.Fatalf("expected status 400, got %d", status)
	}
}

// testEndorserWrongChannel confirms that a proposal for a different channel is rejected at the gRPC level.
func testEndorserWrongChannel(t *testing.T, s *endorserSetup) {
	// Use a client pointing at the same server but asking for a different channel.
	ec, err := network.NewEndorsementClient(
		[]network.PeerConf{{
			Address: s.endorserAddr,
			TLS:     network.TLSConfig{Mode: network.TLSModeNone},
		}},
		testSigner{}, "wrongchannel", s.namespace, "1.0",
	)
	if err != nil {
		t.Fatalf("NewEndorsementClient: %v", err)
	}
	defer ec.Close() //nolint:errcheck

	key := t.Name() + "/k"
	_, err = ec.ExecuteTransaction(t.Context(), s.namespace, "1.0", [][]byte{[]byte("get"), []byte(key)})
	if err == nil {
		t.Fatal("expected error for wrong channel, got nil")
	}
}

// testEndorserSetThenOverwrite writes a key twice and confirms the second value wins.
func testEndorserSetThenOverwrite(t *testing.T, s *endorserSetup) {
	key := t.Name() + "/" + rand.Text()

	currentBlock, _ := s.svc.BlockNumber(t.Context())
	s.proposeAndSubmit(t, [][]byte{[]byte(key), []byte("first")})
	s.waitForBlock(t, currentBlock+1, 5*time.Second)
	s.proposeAndSubmit(t, [][]byte{[]byte(key), []byte("second")})
	s.waitForBlock(t, currentBlock+2, 5*time.Second)

	end, err := s.ec.ExecuteTransaction(t.Context(), s.namespace, "1.0", [][]byte{[]byte(key)})
	if err != nil {
		t.Fatalf("ExecuteTransaction get: %v", err)
	}
	if len(end.Responses) == 0 {
		t.Fatal("no endorsement responses")
	}
	if got := string(end.Responses[0].Response.Payload); got != "second" {
		t.Fatalf("expected %q, got %q", "second", got)
	}
}

// --- special tests ---

// TestWaitForReadyWaitsForSync verifies that the service only reports ready
// after the synchronizer has completed its initial sync with the peer.
// This follows canonical Kubernetes readiness semantics.
func TestWaitForReadyWaitsForSync(t *testing.T) {
	fnet, err := fabrictest.Start(t.Context(), "basic", "fabric-x", fabrictest.Config{})
	if err != nil {
		t.Fatalf("fabrictest.Start: %v", err)
	}

	svcCfg := service.ServiceConfig{
		ChannelID: "mychannel",
		Protocol:  "fabric-x",
		DBConnStr: fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()),
		Committer: network.PeerConf{Address: fmt.Sprintf("127.0.0.1:%d", fnet.PeerPort)},
	}
	svc, err := service.NewWithSigner(svcCfg, testSigner{}, map[string]service.Executor{
		"basic": kvExecutor{},
	}, sdk.NewTestLogger(t, "endorser"))
	if err != nil {
		t.Fatalf("NewWithSigner: %v", err)
	}

	// Start synchronization in background
	syncCtx, syncCancel := context.WithCancel(t.Context())
	defer syncCancel()
	go svc.Run(syncCtx) //nolint:errcheck

	// Service should become ready after sync completes
	if !svc.WaitForReady(t.Context()) {
		t.Fatal("expected service to become ready after synchronization")
	}

	// Should remain ready
	if !svc.WaitForReady(t.Context()) {
		t.Fatal("expected service to remain ready after initial sync")
	}
}
