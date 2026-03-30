/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package integration provides a couple of tests that exercise a large surface
// of the SDK. It provides some assurance on the internal consistency of the
// project. By default, the internal `fabrictest` fake network is used. It can
// also be pointed to real networks.
package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/hyperledger/fabric-x-sdk/network"
	nfab "github.com/hyperledger/fabric-x-sdk/network/fabric"
	nfabx "github.com/hyperledger/fabric-x-sdk/network/fabricx"
	"github.com/hyperledger/fabric-x-sdk/state"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/grpc/grpclog"
	_ "modernc.org/sqlite"
)

const (
	channel   = "mychannel"
	namespace = "mycc"
)

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

// endorse builds a proposal, sends it to endr, and returns the resulting sdk.Endorsement.
func endorse(t *testing.T, signer sdk.Signer, endr *localEndorser, args [][]byte) sdk.Endorsement {
	t.Helper()
	prop, err := network.NewSignedProposal(signer, channel, namespace, "1.0", args, nil)
	if err != nil {
		t.Fatalf("NewSignedProposal: %v", err)
	}
	resp, err := endr.ProcessProposal(context.Background(), prop)
	if err != nil {
		t.Fatalf("ProcessProposal: %v", err)
	}
	proposal, err := protoutil.UnmarshalProposal(prop.ProposalBytes)
	if err != nil {
		t.Fatalf("UnmarshalProposal: %v", err)
	}
	return sdk.Endorsement{Proposal: proposal, Responses: []*peer.ProposalResponse{resp}}
}

// --- test setup ---

type testSetup struct {
	net               *fabrictest.Network
	localDB           *state.VersionedDB
	monotonicVersions bool
	signer            sdk.Signer
	builder           endorsement.Builder
	// submit packages and submits a transaction containing rws to the mock orderer.
	submit func(ctx context.Context, rws blocks.ReadWriteSet) error
	// submitEndorsement submits a pre-built sdk.Endorsement to the mock orderer.
	submitEndorsement func(ctx context.Context, end sdk.Endorsement) error
}

func newSetup(t *testing.T, networkType string, cfg ...fabrictest.BatchingConfig) *testSetup {
	t.Helper()

	// silence GRPC logging
	grpclog.SetLoggerV2(grpclog.NewLoggerV2(io.Discard, os.Stderr, os.Stderr))

	var batchCfg fabrictest.BatchingConfig
	if len(cfg) > 0 {
		batchCfg = cfg[0]
	}
	net, err := fabrictest.Start(namespace, networkType, batchCfg)
	if err != nil {
		t.Fatalf("fabrictest.Start: %v", err)
	}
	t.Cleanup(net.Stop)

	monotonicVersions := networkType == "fabric-x"
	localDB, err := state.NewWriteDB(channel, fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name()))
	if err != nil {
		t.Fatalf("state.NewSqlite: %v", err)
	}
	t.Cleanup(func() { localDB.Close() }) //nolint:errcheck

	signer := testSigner{}
	log := sdk.NewStdLogger("test")
	orderers := []network.OrdererConf{{Address: net.OrdererAddr}}

	var parser blocks.BlockParser
	var builder endorsement.Builder
	var submitter *network.FabricSubmitter
	switch networkType {
	case "fabric":
		parser = bfab.NewBlockParser(log)
		builder = efab.NewEndorsementBuilder(signer)
		submitter, err = nfab.NewSubmitter(orderers, signer, 0)
	case "fabric-x":
		parser = bfabx.NewBlockParser(log)
		builder = efabx.NewEndorsementBuilder(signer)
		submitter, err = nfabx.NewSubmitter(orderers, signer, 0)
	}
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	t.Cleanup(func() { submitter.Close() }) //nolint:errcheck

	// Synchronizer processes and stores blocks.
	processor := blocks.NewProcessor(parser, []blocks.BlockHandler{localDB})
	sync, err := network.NewSynchronizer(localDB, channel, net.PeerAddr, "", signer, processor, sdk.NoOpLogger{})
	if err != nil {
		t.Fatalf("NewSynchronizer: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go sync.Start(ctx) //nolint:errcheck

	// Submit creates, endorses and submits a transaction.
	submit := func(ctx context.Context, rws blocks.ReadWriteSet) error {
		signedProp, err := network.NewSignedProposal(signer, channel, namespace, "1.0", [][]byte{[]byte("invoke")}, nil)
		if err != nil {
			return fmt.Errorf("NewSignedProposal: %w", err)
		}
		inv, err := endorsement.Parse(signedProp, time.Time{})
		if err != nil {
			return fmt.Errorf("endorsement.Parse: %w", err)
		}
		resp, err := builder.Endorse(inv, endorsement.Success(rws, nil, nil))
		if err != nil {
			return fmt.Errorf("Endorse: %w", err)
		}
		end := sdk.Endorsement{Proposal: inv.Proposal, Responses: []*peer.ProposalResponse{resp}}
		return submitter.Submit(ctx, end)
	}

	return &testSetup{
		net:               net,
		localDB:           localDB,
		monotonicVersions: monotonicVersions,
		signer:            signer,
		builder:           builder,
		submit:            submit,
		submitEndorsement: func(ctx context.Context, end sdk.Endorsement) error {
			return submitter.Submit(ctx, end)
		},
	}
}

// --- helpers ---

// waitForKey polls until the key appears in the local DB or the timeout elapses.
// Use this when waiting for block 0 (where BlockNumber() is ambiguous with initial state).
func waitForKey(t *testing.T, db *state.VersionedDB, key string, timeout time.Duration) *blocks.WriteRecord {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		rec, err := db.GetCurrent(namespace, key)
		if err == nil && rec != nil {
			return rec
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for key %q to appear in local DB", key)
	return nil
}

// waitForBlock polls until db.BlockNumber() >= minBlock or the timeout elapses.
// Reliable for minBlock >= 1.
func waitForBlock(t *testing.T, db *state.VersionedDB, minBlock uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		n, _ := db.BlockNumber(context.Background())
		if n >= minBlock {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for block %d in local DB", minBlock)
}

// simulate creates a SimulationStore at blockNum, reads key, and returns the value and RWS.
// Pass blockNum=0 to use the current block height.
func simulate(t *testing.T, db *state.VersionedDB, blockNum uint64, key string, monotonicVersions bool) ([]byte, blocks.ReadWriteSet) {
	t.Helper()
	sim, err := state.NewSimulationStore(context.Background(), db, namespace, blockNum, monotonicVersions)
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
func newSimStore(t *testing.T, db *state.VersionedDB, blockNum uint64, monotonicVersions bool) *state.SimulationStore {
	t.Helper()
	sim, err := state.NewSimulationStore(context.Background(), db, namespace, blockNum, monotonicVersions)
	if err != nil {
		t.Fatalf("NewSimulationStore: %v", err)
	}
	return sim
}

// --- tests ---

// networkTypes lists the two supported network modes under test.
var networkTypes = []string{"fabric", "fabric-x"}

// TestBlindWrite verifies that a write with no prior reads is committed and
// delivered to the local database via the Synchronizer.
func TestBlindWrite(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)

			rws := blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("hello")}},
			}
			if err := s.submit(context.Background(), rws); err != nil {
				t.Fatalf("submit: %v", err)
			}

			rec := waitForKey(t, s.localDB, "k", 5*time.Second)
			if string(rec.Value) != "hello" {
				t.Errorf("got value %q, want %q", rec.Value, "hello")
			}
		})
	}
}

// TestReadModifyWrite verifies that a read-modify-write cycle is committed correctly:
// the simulation captures the read version, and the subsequent write is applied.
func TestReadModifyWrite(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			// 1. Blind write to establish initial state.
			if err := s.submit(ctx, blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v0")}},
			}); err != nil {
				t.Fatalf("blind write: %v", err)
			}
			waitForKey(t, s.localDB, "k", 5*time.Second)

			// 2. Simulate: read "k" (records its version), update to "v1".
			val, rws := simulate(t, s.localDB, 0, "k", s.monotonicVersions)
			if string(val) != "v0" {
				t.Fatalf("expected v0 from simulation, got %q", val)
			}
			rws.Writes = []blocks.KVWrite{{Key: "k", Value: []byte("v1")}}

			if err := s.submit(ctx, rws); err != nil {
				t.Fatalf("submit read-modify-write: %v", err)
			}
			waitForBlock(t, s.localDB, 2, 5*time.Second)

			rec, err := s.localDB.GetCurrent(namespace, "k")
			if err != nil {
				t.Fatalf("GetCurrent: %v", err)
			}
			if string(rec.Value) != "v1" {
				t.Errorf("got value %q, want %q", rec.Value, "v1")
			}
		})
	}
}

// TestMVCCConflict verifies that when two transactions read the same key at the
// same version and both try to write it, the second is rejected by MVCC.
func TestMVCCConflict(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			// 1. Blind write to establish initial state (block 0, version 0).
			if err := s.submit(ctx, blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v0")}},
			}); err != nil {
				t.Fatalf("blind write: %v", err)
			}
			waitForKey(t, s.localDB, "k", 5*time.Second)

			// 2. Simulate tx1 and tx2 both reading "k" at the same snapshot (version 0).
			_, rws1 := simulate(t, s.localDB, 0, "k", s.monotonicVersions)
			rws1.Writes = []blocks.KVWrite{{Key: "k", Value: []byte("v1")}}

			_, rws2 := simulate(t, s.localDB, 0, "k", s.monotonicVersions)
			rws2.Writes = []blocks.KVWrite{{Key: "k", Value: []byte("v2")}}

			// 3. Submit both. The second should be marked as an MVCC conflict.
			// Both may land in the same block; intra-block validation (first writer wins)
			// ensures tx2 is rejected even within that block.
			if err := s.submit(ctx, rws1); err != nil {
				t.Fatalf("submit tx1: %v", err)
			}
			if err := s.submit(ctx, rws2); err != nil {
				t.Fatalf("submit tx2 (orderer-level success expected): %v", err)
			}
			waitForBlock(t, s.localDB, 2, 5*time.Second)

			// 4. Only tx1's write should be in the state; tx2 was rejected.
			rec, err := s.localDB.GetCurrent(namespace, "k")
			if err != nil {
				t.Fatalf("GetCurrent: %v", err)
			}
			if string(rec.Value) != "v1" {
				t.Errorf("got %q, want %q — tx2 should have been rejected by MVCC", rec.Value, "v1")
			}
		})
	}
}

// TestDelete verifies that a key "deleted" via simulation is committed and
// visible in the local database. Fabric marks it with IsDelete=true; Fabric-X
// has no deletion concept so the key is stored with a nil value instead.
func TestDelete(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			// 1. Blind write to establish initial state.
			if err := s.submit(ctx, blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v0")}},
			}); err != nil {
				t.Fatalf("blind write: %v", err)
			}
			waitForKey(t, s.localDB, "k", 5*time.Second)

			// 2. Simulate: read "k" (records version), then delete it.
			_, rws := simulate(t, s.localDB, 0, "k", s.monotonicVersions)
			rws.Writes = []blocks.KVWrite{{Key: "k", IsDelete: true}}

			if err := s.submit(ctx, rws); err != nil {
				t.Fatalf("submit delete: %v", err)
			}
			waitForBlock(t, s.localDB, 2, 5*time.Second)

			rec, err := s.localDB.GetCurrent(namespace, "k")
			if err != nil {
				t.Fatalf("GetCurrent: %v", err)
			}
			switch nt {
			case "fabric":
				if rec == nil || !rec.IsDelete {
					t.Errorf("expected IsDelete=true, got %+v", rec)
				}
			case "fabric-x":
				// Fabric-X stores nil value as NULL; there is no deletion tombstone.
				if rec == nil || rec.IsDelete || rec.Value != nil {
					t.Errorf("expected nil value with IsDelete=false, got %+v", rec)
				}
			}
		})
	}
}

// TestSimulationPutAndDelState verifies that SimulationStore.PutState and DelState
// correctly generate write sets that commit through the full pipeline, including
// multi-key transactions.
func TestSimulationPutAndDelState(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			// 1. PutState two keys in a single transaction.
			sim := newSimStore(t, s.localDB, 0, s.monotonicVersions)
			if err := sim.PutState("alpha", []byte("a")); err != nil {
				t.Fatalf("PutState alpha: %v", err)
			}
			if err := sim.PutState("beta", []byte("b")); err != nil {
				t.Fatalf("PutState beta: %v", err)
			}
			if err := s.submit(ctx, sim.Result()); err != nil {
				t.Fatalf("submit: %v", err)
			}
			waitForKey(t, s.localDB, "alpha", 5*time.Second)
			waitForKey(t, s.localDB, "beta", 5*time.Second)

			recAlpha, _ := s.localDB.GetCurrent(namespace, "alpha")
			recBeta, _ := s.localDB.GetCurrent(namespace, "beta")
			if string(recAlpha.Value) != "a" {
				t.Errorf("alpha: got %q, want %q", recAlpha.Value, "a")
			}
			if string(recBeta.Value) != "b" {
				t.Errorf("beta: got %q, want %q", recBeta.Value, "b")
			}

			// 2. DelState "alpha" in a second transaction; "beta" must be unaffected.
			sim2 := newSimStore(t, s.localDB, 0, s.monotonicVersions)
			if _, err := sim2.GetState("alpha"); err != nil {
				t.Fatalf("GetState alpha: %v", err)
			}
			if err := sim2.DelState("alpha"); err != nil {
				t.Fatalf("DelState alpha: %v", err)
			}
			if err := s.submit(ctx, sim2.Result()); err != nil {
				t.Fatalf("submit delete: %v", err)
			}
			waitForBlock(t, s.localDB, 2, 5*time.Second)

			recAlpha, err := s.localDB.GetCurrent(namespace, "alpha")
			if err != nil {
				t.Fatalf("GetCurrent alpha: %v", err)
			}
			switch nt {
			case "fabric":
				if recAlpha == nil || !recAlpha.IsDelete {
					t.Errorf("expected IsDelete=true for alpha, got %+v", recAlpha)
				}
			case "fabric-x":
				if recAlpha == nil || recAlpha.IsDelete || recAlpha.Value != nil {
					t.Errorf("expected nil value with IsDelete=false for alpha, got %+v", recAlpha)
				}
			}
			recBeta, err = s.localDB.GetCurrent(namespace, "beta")
			if err != nil {
				t.Fatalf("GetCurrent beta: %v", err)
			}
			if string(recBeta.Value) != "b" {
				t.Errorf("beta: got %q, want %q after alpha deletion", recBeta.Value, "b")
			}
		})
	}
}

// TestGetHistory verifies that VersionedDB.GetHistory returns all versions of a
// key in ascending block order after a sequence of writes.
func TestGetHistory(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			values := []string{"v0", "v1", "v2"}

			// Write v0 as a blind write (block 0).
			if err := s.submit(ctx, blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v0")}},
			}); err != nil {
				t.Fatalf("blind write: %v", err)
			}
			waitForKey(t, s.localDB, "k", 5*time.Second)

			// Write v1 and v2 each as a read-modify-write (blocks 2 and 3).
			// Wait for each block before simulating the next to ensure they land
			// in separate blocks and history is strictly ascending.
			for i, val := range values[1:] {
				_, rws := simulate(t, s.localDB, 0, "k", s.monotonicVersions)
				rws.Writes = []blocks.KVWrite{{Key: "k", Value: []byte(val)}}
				if err := s.submit(ctx, rws); err != nil {
					t.Fatalf("submit %s: %v", val, err)
				}
				waitForBlock(t, s.localDB, uint64(i+2), 5*time.Second)
			}

			history, err := s.localDB.GetHistory(namespace, "k")
			if err != nil {
				t.Fatalf("GetHistory: %v", err)
			}
			if len(history) != len(values) {
				t.Fatalf("got %d history records, want %d", len(history), len(values))
			}
			for i, rec := range history {
				if string(rec.Value) != values[i] {
					t.Errorf("history[%d]: got %q, want %q", i, rec.Value, values[i])
				}
				if i > 0 && rec.BlockNum <= history[i-1].BlockNum {
					t.Errorf("history not in ascending block order at index %d", i)
				}
			}
		})
	}
}

// TestPointInTimeSimulation verifies that a SimulationStore snapshot captures
// state at a specific block height, and that a RWS derived from a stale snapshot
// is rejected by MVCC once the key has advanced to a newer version.
func TestPointInTimeSimulation(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			// 1. Blind write "v0" (block 0).
			if err := s.submit(ctx, blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v0")}},
			}); err != nil {
				t.Fatalf("blind write: %v", err)
			}
			waitForKey(t, s.localDB, "k", 5*time.Second)

			// 2. Snapshot at block 0 — records the read version of "k".
			val, staleRWS := simulate(t, s.localDB, 0, "k", s.monotonicVersions)
			if string(val) != "v0" {
				t.Fatalf("expected v0 from snapshot, got %q", val)
			}
			staleRWS.Writes = []blocks.KVWrite{{Key: "k", Value: []byte("v_stale")}}

			// 3. Advance "k" to "v1" (block 2). This invalidates the snapshot's read version.
			// Wait for block 2 to commit before submitting the stale RWS so that
			// it lands in block 3 where MVCC can compare against the updated version.
			if err := s.submit(ctx, blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v1")}},
			}); err != nil {
				t.Fatalf("submit v1: %v", err)
			}
			waitForBlock(t, s.localDB, 2, 5*time.Second)

			// 4. Submit the stale RWS. MVCC must reject it because "k" has advanced
			// past the version recorded in the snapshot.
			if err := s.submit(ctx, staleRWS); err != nil {
				t.Fatalf("submit stale rws (orderer-level success expected): %v", err)
			}
			waitForBlock(t, s.localDB, 3, 5*time.Second)

			rec, err := s.localDB.GetCurrent(namespace, "k")
			if err != nil {
				t.Fatalf("GetCurrent: %v", err)
			}
			if string(rec.Value) != "v1" {
				t.Errorf("got %q, want %q — stale snapshot tx should have been rejected by MVCC", rec.Value, "v1")
			}
		})
	}
}

// TestAddLog verifies that SimulationStore.AddLog and Logs() work
// correctly and that event bytes can be passed through the endorsement pipeline.
func TestAddLog(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			sim := newSimStore(t, s.localDB, 0, s.monotonicVersions)
			if err := sim.PutState("k", []byte("with-log")); err != nil {
				t.Fatalf("PutState: %v", err)
			}
			sim.AddLog(
				[]byte("0xcontract"),
				[][]byte{[]byte("Transfer"), []byte("0xfrom"), []byte("0xto")},
				[]byte(`{"amount":42}`),
			)

			logs := sim.Logs()
			if len(logs) != 1 {
				t.Fatalf("expected 1 log, got %d", len(logs))
			}
			eventBytes, err := json.Marshal(logs)
			if err != nil {
				t.Fatalf("marshal logs: %v", err)
			}

			signedProp, err := network.NewSignedProposal(s.signer, channel, namespace, "1.0", [][]byte{[]byte("invoke")}, nil)
			if err != nil {
				t.Fatalf("NewSignedProposal: %v", err)
			}
			inv, err := endorsement.Parse(signedProp, time.Time{})
			if err != nil {
				t.Fatalf("endorsement.Parse: %v", err)
			}
			resp, err := s.builder.Endorse(inv, endorsement.Success(sim.Result(), eventBytes, nil))
			if err != nil {
				t.Fatalf("Endorse: %v", err)
			}
			end := sdk.Endorsement{Proposal: inv.Proposal, Responses: []*peer.ProposalResponse{resp}}
			if err := s.submitEndorsement(ctx, end); err != nil {
				t.Fatalf("submitEndorsement: %v", err)
			}

			rec := waitForKey(t, s.localDB, "k", 5*time.Second)
			if string(rec.Value) != "with-log" {
				t.Errorf("got %q, want %q", rec.Value, "with-log")
			}
		})
	}
}

// TestEndorsementClientExecuteTransaction verifies that EndorsementClient.ExecuteTransaction
// creates a proposal, collects a response from the endorser, and returns an sdk.Endorsement
// that can be submitted to the orderer and committed.
func TestEndorsementClientExecuteTransaction(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)
			ctx := context.Background()

			endr := &localEndorser{
				builder: s.builder,
				result: func(_ endorsement.Invocation) endorsement.ExecutionResult {
					return endorsement.Success(blocks.ReadWriteSet{
						Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v_ec")}},
					}, nil, nil)
				},
			}

			end := endorse(t, s.signer, endr, [][]byte{[]byte("invoke")})
			if err := s.submitEndorsement(ctx, end); err != nil {
				t.Fatalf("submitEndorsement: %v", err)
			}

			rec := waitForKey(t, s.localDB, "k", 5*time.Second)
			if string(rec.Value) != "v_ec" {
				t.Errorf("got %q, want %q", rec.Value, "v_ec")
			}
		})
	}
}

// TestBadRequestEndorsement verifies that endorsement.BadRequest produces a
// ProposalResponse with HTTP 400 status that propagates correctly through
// EndorsementClient.
func TestBadRequestEndorsement(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt)

			endr := &localEndorser{
				builder: s.builder,
				result: func(_ endorsement.Invocation) endorsement.ExecutionResult {
					return endorsement.BadRequest("unauthorized")
				},
			}

			// endorse does not inspect response status — it returns the
			// raw responses so callers can decide how to handle them.
			end := endorse(t, s.signer, endr, [][]byte{[]byte("invoke")})

			if len(end.Responses) != 1 {
				t.Fatalf("expected 1 response, got %d", len(end.Responses))
			}
			resp := end.Responses[0]
			if resp.Response == nil {
				t.Fatal("response is nil")
			}
			if resp.Response.Status != 400 {
				t.Errorf("got status %d, want 400", resp.Response.Status)
			}
		})
	}
}

// TestTwoTransactions tests two transactions in the same block with blind writes.
func TestTwoTransactions(t *testing.T) {
	for _, nt := range networkTypes {
		t.Run(nt, func(t *testing.T) {
			s := newSetup(t, nt, fabrictest.BatchingConfig{BlockCutTime: time.Second, MaxTxPerBlock: 2})

			rws := blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k", Value: []byte("hello")}},
			}
			if err := s.submit(t.Context(), rws); err != nil {
				t.Fatalf("submit: %v", err)
			}

			rws = blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: "k1", Value: []byte("hello1")}},
			}
			if err := s.submit(t.Context(), rws); err != nil {
				t.Fatalf("submit: %v", err)
			}

			rec := waitForKey(t, s.localDB, "k", 5*time.Second)
			if string(rec.Value) != "hello" {
				t.Errorf("got value %q, want %q", rec.Value, "hello")
			}
			rec = waitForKey(t, s.localDB, "k1", 1*time.Second)
			if string(rec.Value) != "hello1" {
				t.Errorf("got value %q, want %q", rec.Value, "hello1")
			}
		})
	}
}
