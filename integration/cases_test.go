/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package integration

import (
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric-x-sdk/network"
	"google.golang.org/protobuf/proto"
)

// testCase is a backend-agnostic test function. The testSetup is shared across
// all cases within one top-level test, so each case must use unique keys.
type testCase struct {
	name string
	fn   func(*testing.T, *testSetup)
}

// cases is the shared test corpus run against every backend.
// Adding a new test case here is the only change required.
var cases = []testCase{
	{"BlindWrite", testBlindWrite},
	{"ReadModifyWrite", testReadModifyWrite},
	{"MVCCConflict", testMVCCConflict},
	{"Delete", testDelete},
	{"SimulationPutAndDelState", testSimulationPutAndDelState},
	{"GetHistory", testGetHistory},
	{"PointInTimeSimulation", testPointInTimeSimulation},
	{"AddLog", testAddLog},
	{"EndorsementClientExecute", testEndorsementClientExecuteTransaction},
	{"BadRequestEndorsement", testBadRequestEndorsement},
	{"InputArgsAndEvents", testInputArgsAndEvents},
}

// runAll executes every case as a subtest against s.
// The setup is shared; isolation is achieved via unique keys per case.
func runAll(t *testing.T, s *testSetup) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, s)
		})
	}
}

// --- test cases ---

func testBlindWrite(t *testing.T, s *testSetup) {
	key := t.Name() + "/" + rand.Text()
	if err := s.endorseAndSubmit(t.Context(), blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("hello")}},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s.waitForKeyValue(t, key, "hello")
}

func testReadModifyWrite(t *testing.T, s *testSetup) {
	ctx := t.Context()
	key := t.Name() + "/" + rand.Text()

	if err := s.endorseAndSubmit(ctx, blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("v0")}},
	}); err != nil {
		t.Fatalf("blind write: %v", err)
	}
	s.waitForKeyValue(t, key, "v0")

	val, rws := s.simulate(t, 0, key)
	if string(val) != "v0" {
		t.Fatalf("expected v0 from simulation, got %q", val)
	}
	rws.Writes = []blocks.KVWrite{{Key: key, Value: []byte("v1")}}
	if err := s.endorseAndSubmit(ctx, rws); err != nil {
		t.Fatalf("submit read-modify-write: %v", err)
	}
	s.waitForKeyValue(t, key, "v1")
}

func testMVCCConflict(t *testing.T, s *testSetup) {
	ctx := t.Context()
	prefix := t.Name() + "/" + rand.Text()
	key := prefix + "/k"

	if err := s.endorseAndSubmit(ctx, blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("v0")}},
	}); err != nil {
		t.Fatalf("blind write: %v", err)
	}
	s.waitForKeyValue(t, key, "v0")

	_, rws1 := s.simulate(t, 0, key)
	rws1.Writes = []blocks.KVWrite{{Key: key, Value: []byte("v1")}}
	_, rws2 := s.simulate(t, 0, key)
	rws2.Writes = []blocks.KVWrite{{Key: key, Value: []byte("v2")}}

	if err := s.endorseAndSubmit(ctx, rws1); err != nil {
		t.Fatalf("submit tx1: %v", err)
	}
	if err := s.endorseAndSubmit(ctx, rws2); err != nil {
		t.Fatalf("submit tx2 (orderer-level success expected): %v", err)
	}
	// The sentinel is submitted after both conflicting transactions.
	// When it is committed, tx1 and tx2 have definitely been processed.
	s.sentinel(t, prefix+"/sentinel")

	rec, err := s.localDB.GetCurrent(s.namespace, key)
	if err != nil {
		t.Fatalf("GetCurrent: %v", err)
	}
	if string(rec.Value) != "v1" {
		t.Errorf("got %q, want %q — tx2 should have been rejected by MVCC", rec.Value, "v1")
	}
}

func testDelete(t *testing.T, s *testSetup) {
	ctx := t.Context()
	prefix := t.Name() + "/" + rand.Text()
	key := prefix + "/k"

	if err := s.endorseAndSubmit(ctx, blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("v0")}},
	}); err != nil {
		t.Fatalf("blind write: %v", err)
	}
	s.waitForKeyValue(t, key, "v0")

	_, rws := s.simulate(t, 0, key)
	rws.Writes = []blocks.KVWrite{{Key: key, IsDelete: true}}
	if err := s.endorseAndSubmit(ctx, rws); err != nil {
		t.Fatalf("submit delete: %v", err)
	}
	s.sentinel(t, prefix+"/sentinel")

	rec, err := s.localDB.GetCurrent(s.namespace, key)
	if err != nil {
		t.Fatalf("GetCurrent: %v", err)
	}
	switch s.networkType {
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
}

func testSimulationPutAndDelState(t *testing.T, s *testSetup) {
	ctx := t.Context()
	prefix := t.Name() + "/" + rand.Text()
	keyAlpha := prefix + "/alpha"
	keyBeta := prefix + "/beta"

	sim := s.newSimStore(t, 0)
	if err := sim.PutState(keyAlpha, []byte("a")); err != nil {
		t.Fatalf("PutState alpha: %v", err)
	}
	if err := sim.PutState(keyBeta, []byte("b")); err != nil {
		t.Fatalf("PutState beta: %v", err)
	}
	if err := s.endorseAndSubmit(ctx, sim.Result()); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s.waitForKeyValue(t, keyAlpha, "a")
	s.waitForKeyValue(t, keyBeta, "b")

	sim2 := s.newSimStore(t, 0)
	if _, err := sim2.GetState(keyAlpha); err != nil {
		t.Fatalf("GetState alpha: %v", err)
	}
	if err := sim2.DelState(keyAlpha); err != nil {
		t.Fatalf("DelState alpha: %v", err)
	}
	if err := s.endorseAndSubmit(ctx, sim2.Result()); err != nil {
		t.Fatalf("submit delete: %v", err)
	}
	s.sentinel(t, prefix+"/sentinel")

	recAlpha, err := s.localDB.GetCurrent(s.namespace, keyAlpha)
	if err != nil {
		t.Fatalf("GetCurrent alpha: %v", err)
	}
	switch s.networkType {
	case "fabric":
		if recAlpha == nil || !recAlpha.IsDelete {
			t.Errorf("expected IsDelete=true for alpha, got %+v", recAlpha)
		}
	case "fabric-x":
		if recAlpha == nil || recAlpha.IsDelete || recAlpha.Value != nil {
			t.Errorf("expected nil value with IsDelete=false for alpha, got %+v", recAlpha)
		}
	}
	recBeta, err := s.localDB.GetCurrent(s.namespace, keyBeta)
	if err != nil {
		t.Fatalf("GetCurrent beta: %v", err)
	}
	if string(recBeta.Value) != "b" {
		t.Errorf("beta: got %q, want %q after alpha deletion", recBeta.Value, "b")
	}
}

// testGetHistory uses waitForBlock with relative offsets because the test
// asserts strictly ascending block numbers in the history — it cannot be
// replaced by waitForKeyValue.
func testGetHistory(t *testing.T, s *testSetup) {
	ctx := t.Context()
	key := t.Name() + "/" + rand.Text()
	values := []string{"v0", "v1", "v2"}

	base, _ := s.localDB.BlockNumber(ctx)

	if err := s.endorseAndSubmit(ctx, blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("v0")}},
	}); err != nil {
		t.Fatalf("blind write: %v", err)
	}
	s.waitForKeyValue(t, key, "v0")

	// Submit v1 and v2 in separate blocks: wait for each block before
	// submitting the next so history has strictly ascending block numbers.
	for i, val := range values[1:] {
		_, rws := s.simulate(t, 0, key)
		rws.Writes = []blocks.KVWrite{{Key: key, Value: []byte(val)}}
		if err := s.endorseAndSubmit(ctx, rws); err != nil {
			t.Fatalf("submit %s: %v", val, err)
		}
		s.waitForBlock(t, base+uint64(i+2))
	}

	history, err := s.localDB.GetHistory(s.namespace, key)
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
}

func testPointInTimeSimulation(t *testing.T, s *testSetup) {
	ctx := t.Context()
	prefix := t.Name() + "/" + rand.Text()
	key := prefix + "/k"

	base, _ := s.localDB.BlockNumber(ctx)

	if err := s.endorseAndSubmit(ctx, blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("v0")}},
	}); err != nil {
		t.Fatalf("blind write: %v", err)
	}
	s.waitForKeyValue(t, key, "v0")

	val, staleRWS := s.simulate(t, 0, key)
	if string(val) != "v0" {
		t.Fatalf("expected v0 from snapshot, got %q", val)
	}
	staleRWS.Writes = []blocks.KVWrite{{Key: key, Value: []byte("v_stale")}}

	// Advance key to v1 and wait for it to be committed so the stale
	// snapshot's read version is definitely outdated when the stale RWS arrives.
	if err := s.endorseAndSubmit(ctx, blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("v1")}},
	}); err != nil {
		t.Fatalf("submit v1: %v", err)
	}
	s.waitForBlock(t, base+2)

	if err := s.endorseAndSubmit(ctx, staleRWS); err != nil {
		t.Fatalf("submit stale rws (orderer-level success expected): %v", err)
	}
	// Sentinel ensures the stale RWS has been processed before we inspect the key.
	s.sentinel(t, prefix+"/sentinel")

	rec, err := s.localDB.GetCurrent(s.namespace, key)
	if err != nil {
		t.Fatalf("GetCurrent: %v", err)
	}
	if string(rec.Value) != "v1" {
		t.Errorf("got %q, want %q — stale snapshot tx should have been rejected by MVCC", rec.Value, "v1")
	}
}

func testAddLog(t *testing.T, s *testSetup) {
	ctx := t.Context()
	key := t.Name() + "/" + rand.Text()

	sim := s.newSimStore(t, 0)
	if err := sim.PutState(key, []byte("with-log")); err != nil {
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

	signedProp, err := network.NewSignedProposal(s.signer, s.channel, s.namespace, "1.0", [][]byte{[]byte("invoke")})
	if err != nil {
		t.Fatalf("NewSignedProposal: %v", err)
	}
	inv, err := endorsement.Parse(signedProp, time.Time{})
	if err != nil {
		t.Fatalf("endorsement.Parse: %v", err)
	}
	var responses []*peer.ProposalResponse
	for _, b := range s.builders {
		resp, err := b.Endorse(inv, endorsement.Success(sim.Result(), eventBytes, nil))
		if err != nil {
			t.Fatalf("Endorse: %v", err)
		}
		responses = append(responses, resp)
	}
	end := sdk.Endorsement{Proposal: inv.Proposal, Responses: responses}
	if err := s.submitter.Submit(ctx, end); err != nil {
		t.Fatalf("submitEndorsement: %v", err)
	}
	s.waitForKeyValue(t, key, "with-log")
}

func testEndorsementClientExecuteTransaction(t *testing.T, s *testSetup) {
	key := t.Name() + "/" + rand.Text()

	endr := &localEndorser{
		result: func(_ endorsement.Invocation) endorsement.ExecutionResult {
			return endorsement.Success(blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{{Key: key, Value: []byte("v_ec")}},
			}, nil, nil)
		},
	}

	end := s.endorse(t, endr, [][]byte{[]byte("invoke")})
	if err := s.submitter.Submit(t.Context(), end); err != nil {
		t.Fatalf("submitEndorsement: %v", err)
	}
	s.waitForKeyValue(t, key, "v_ec")
}

func testBadRequestEndorsement(t *testing.T, s *testSetup) {
	endr := &localEndorser{
		result: func(_ endorsement.Invocation) endorsement.ExecutionResult {
			return endorsement.BadRequest("unauthorized")
		},
	}

	end := s.endorse(t, endr, [][]byte{[]byte("invoke")})
	if len(end.Responses) == 0 {
		t.Fatal("expected at least one response")
	}
	resp := end.Responses[0]
	if resp.Response == nil {
		t.Fatal("response is nil")
	}
	if resp.Response.Status != 400 {
		t.Errorf("got status %d, want 400", resp.Response.Status)
	}
}

func testInputArgsAndEvents(t *testing.T, s *testSetup) {
	ctx := t.Context()
	key := t.Name() + "/" + rand.Text()
	args := [][]byte{[]byte("invoke"), []byte("arg1"), []byte("arg2")}
	eventPayload := []byte(`{"type":"Transfer"}`)

	signedProp, err := network.NewSignedProposal(s.signer, s.channel, s.namespace, "1.0", args)
	if err != nil {
		t.Fatalf("NewSignedProposal: %v", err)
	}
	inv, err := endorsement.Parse(signedProp, time.Time{})
	if err != nil {
		t.Fatalf("endorsement.Parse: %v", err)
	}

	var responses []*peer.ProposalResponse
	for _, b := range s.builders {
		resp, err := b.Endorse(inv, endorsement.Success(blocks.ReadWriteSet{
			Writes: []blocks.KVWrite{{Key: key, Value: []byte("v")}},
		}, eventPayload, nil))
		if err != nil {
			t.Fatalf("Endorse: %v", err)
		}
		responses = append(responses, resp)
	}

	end := sdk.Endorsement{Proposal: inv.Proposal, Responses: responses}
	if err := s.submitter.Submit(ctx, end); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// waitForKeyValue establishes finality: handlers run sequentially, so capture
	// is guaranteed to have seen the block by the time this returns.
	s.waitForKeyValue(t, key, "v")
	tx := s.capture.findTx(inv.TxID)
	if tx == nil {
		t.Fatalf("tx %q not found in captured blocks", inv.TxID)
	}

	// input args
	if len(tx.InputArgs) != len(args) {
		t.Fatalf("InputArgs: got %d elements, want %d", len(tx.InputArgs), len(args))
	}
	for i, arg := range args {
		if string(tx.InputArgs[i]) != string(arg) {
			t.Errorf("InputArgs[%d]: got %q, want %q", i, tx.InputArgs[i], arg)
		}
	}

	// events
	if len(tx.Events) == 0 {
		t.Fatal("Events: expected non-empty")
	}
	evt := &peer.ChaincodeEvent{}
	if err := proto.Unmarshal(tx.Events, evt); err != nil {
		t.Fatalf("unmarshal Events: %v", err)
	}
	if string(evt.Payload) != string(eventPayload) {
		t.Errorf("event payload: got %q, want %q", evt.Payload, eventPayload)
	}
}

func testTwoTransactions(t *testing.T, s *testSetup) {
	prefix := t.Name() + "/" + rand.Text()
	key := prefix + "/k"
	key1 := prefix + "/k1"

	if err := s.endorseAndSubmit(t.Context(), blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key, Value: []byte("hello")}},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := s.endorseAndSubmit(t.Context(), blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: key1, Value: []byte("hello1")}},
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	s.waitForKeyValue(t, key, "hello")
	s.waitForKeyValue(t, key1, "hello1")
}
