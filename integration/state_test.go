/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package integration

import (
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric-x-sdk/state"
	"github.com/hyperledger/fabric-x-sdk/state/queryservice"
)

// testState runs sequences of transactions and checks, after every
// transaction, that each state backend holds exactly the expected values and
// versions. On the real committer the query service reads the committer's own
// state; the local backends are fed the committed blocks. Simulating on every
// backend also checks the values and read versions an endorser would see.
//
// The expectations hold on the real Fabric-X committer, so on fabrictest the
// same cases check that the fake behaves like it.
func testState(t *testing.T, s *testSetup) {
	if s.networkType != "fabric-x" {
		t.Skip("state cases encode Fabric-X versions and deletes")
	}

	var backends []kvsBackend
	if s.queryService.Address != "" {
		client, err := queryservice.NewGRPCClient(s.queryService, 1)
		if err != nil {
			t.Fatalf("query service client: %v", err)
		}
		qs := queryservice.NewStore(client)
		t.Cleanup(func() { qs.Close() }) //nolint:errcheck,gosec // Best-effort test cleanup.
		backends = append(backends, kvsBackend{"queryservice", qs})
	}
	backends = append(backends, kvsBackend{"sqlite", s.localDB})

	for _, tc := range stateCases {
		t.Run(tc.name, func(t *testing.T) { runStateCase(t, s, backends, tc) })
	}
}

// stateCases are the sequences run by testState. Keys are local
// to a case. A step's want lists only the keys it changes; every other key the
// case uses keeps its previous expected state, or stays absent.
//
// On Fabric-X a new key starts at version 0 and every write, including a
// delete, bumps it. A delete writes a NULL value and keeps the row, so a deleted
// key is still read with its version.
var stateCases = []stateCase{
	{"Insert", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
	}},
	{"BlindOverwrite", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(put("a", "v1")), want: kv{"a": is("v1", 1)}},
	}},
	{"ReadModifyWrite", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(get("a"), put("a", "v1")), want: kv{"a": is("v1", 1)}},
	}},
	{"ReadMissingThenInsert", []step{
		{do: ops(get("a"), put("a", "v0")), want: kv{"a": is("v0", 0)}},
	}},
	{"DeleteExisting", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(get("a"), del("a")), want: kv{"a": null(1)}},
	}},
	{"EmptyValueIsDelete", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(put("a", "")), want: kv{"a": null(1)}},
	}},
	{"DeleteNeverWritten", []step{
		{do: ops(del("a")), want: kv{"a": null(0)}},
	}},
	{"ReadDeletedThenWrite", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(del("a")), want: kv{"a": null(1)}},
		{do: ops(get("a"), put("a", "v2")), want: kv{"a": is("v2", 2)}},
	}},
	{"BlindRewriteAfterDelete", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(del("a")), want: kv{"a": null(1)}},
		{do: ops(put("a", "v2")), want: kv{"a": is("v2", 2)}},
	}},
	{"StaleReadConflicts", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(get("a"), put("a", "v1")), want: kv{"a": is("v1", 1)}},
		{stale: true, do: ops(get("a"), put("a", "v2")), conflict: true},
	}},
	{"StaleMissingReadConflicts", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{stale: true, do: ops(get("a"), put("b", "v0")), conflict: true},
	}},
	{"StaleReadOfDeletedConflicts", []step{
		{do: ops(put("a", "v0")), want: kv{"a": is("v0", 0)}},
		{do: ops(del("a")), want: kv{"a": null(1)}},
		{stale: true, do: ops(get("a"), put("b", "v0")), conflict: true},
	}},
	{"ManyKeys", []step{
		{do: ops(put("a", "v0"), put("b", "v0")), want: kv{"a": is("v0", 0), "b": is("v0", 0)}},
		{
			do:   ops(get("a"), get("c"), put("a", "v1"), del("b"), put("d", "v0")),
			want: kv{"a": is("v1", 1), "b": null(1), "d": is("v0", 0)},
		},
	}},
}

type stateCase struct {
	name  string
	steps []step
}

// step is one transaction. A stale step is simulated together with the step
// before it, before that one is submitted, so it reads the state as it was.
type step struct {
	stale    bool
	do       []op
	conflict bool // the committer rejects it with an MVCC conflict
	want     kv
}

type op struct {
	kind       byte // 'g'et, 'p'ut or 'd'el
	key, value string
}

func get(key string) op        { return op{'g', key, ""} }
func put(key, value string) op { return op{'p', key, value} }
func del(key string) op        { return op{'d', key, ""} }
func ops(o ...op) []op         { return o }

// kv maps a case-local key to its expected state.
type kv map[string]entry

// entry is a key's expected state: a value, or NULL after a delete.
type entry struct {
	value   string
	null    bool
	version uint64
}

func is(value string, version uint64) entry { return entry{value: value, version: version} }
func null(version uint64) entry             { return entry{null: true, version: version} }

type kvsBackend struct {
	name string
	kvs  state.KVS
}

// stateRun is the state of one case while it runs.
type stateRun struct {
	s        *testSetup
	backends []kvsBackend
	prefix   string
	keys     []string // case-local keys, sorted
}

func runStateCase(t *testing.T, s *testSetup, backends []kvsBackend, tc stateCase) {
	r := stateRun{s: s, backends: backends, prefix: t.Name() + "/" + rand.Text() + "/"}
	seen := map[string]bool{}
	for _, st := range tc.steps {
		for _, o := range st.do {
			seen[o.key] = true
		}
	}
	r.keys = slices.Sorted(maps.Keys(seen))

	want := kv{}
	var history []snapshotWant
	var early blocks.ReadWriteSet
	for i, st := range tc.steps {
		rws := early
		if !st.stale {
			rws = r.simulate(t, i, st.do, want)
		}
		if i+1 < len(tc.steps) && tc.steps[i+1].stale {
			early = r.simulate(t, i+1, tc.steps[i+1].do, want)
		}

		tx := r.submit(t, rws)
		switch {
		case st.conflict && tx.Status != blocks.StatusMVCCConflict:
			t.Fatalf("step %d: status %v (%s), want MVCC conflict", i, tx.Status, tx.Reason)
		case !st.conflict && !tx.Valid():
			t.Fatalf("step %d: status %v (%s), want committed", i, tx.Status, tx.Reason)
		}

		maps.Copy(want, st.want)
		r.checkLatest(t, i, want)
		block, err := s.localDB.BlockNumber(t.Context())
		if err != nil {
			t.Fatalf("BlockNumber: %v", err)
		}
		history = append(history, snapshotWant{block, maps.Clone(want)})
	}
	r.checkHistory(t, history)
}

// simulate runs ops on a fresh snapshot of every backend, checks each read
// against want, and returns the read-write set of the first backend.
func (r *stateRun) simulate(t *testing.T, stepIdx int, do []op, want kv) blocks.ReadWriteSet {
	t.Helper()
	var first blocks.ReadWriteSet
	for i, b := range r.backends {
		snap, err := b.kvs.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("%s: Snapshot: %v", b.name, err)
		}
		block, err := b.kvs.BlockNumber(t.Context())
		if err != nil {
			t.Fatalf("%s: BlockNumber: %v", b.name, err)
		}
		sim := state.NewSimulationStore(snap, r.s.namespace, block, r.s.monotonicVersions)
		for _, o := range do {
			var err error
			switch o.kind {
			case 'g':
				var val []byte
				val, err = sim.GetState(r.prefix + o.key)
				if err == nil && string(val) != want[o.key].value {
					t.Errorf("step %d: %s: GetState(%s) = %q, want %q", stepIdx, b.name, o.key, val, want[o.key].value)
				}
			case 'p':
				err = sim.PutState(r.prefix+o.key, []byte(o.value))
			case 'd':
				err = sim.DelState(r.prefix + o.key)
			}
			if err != nil {
				t.Fatalf("step %d: %s: %c %s: %v", stepIdx, b.name, o.kind, o.key, err)
			}
		}
		if err := snap.Close(); err != nil {
			t.Fatalf("%s: close snapshot: %v", b.name, err)
		}

		rws := sim.Result()
		for _, rd := range rws.Reads {
			key := rd.Key[len(r.prefix):]
			got, wantVersion := "nil", "nil"
			if rd.Version != nil {
				got = fmt.Sprint(rd.Version.BlockNum)
			}
			if e, exists := want[key]; exists {
				wantVersion = fmt.Sprint(e.version)
			}
			if got != wantVersion {
				t.Errorf("step %d: %s: read version of %s = %s, want %s", stepIdx, b.name, key, got, wantVersion)
			}
		}
		if i == 0 {
			first = rws
		}
	}
	return first
}

// submit endorses rws, submits it, and waits until its block has been
// processed, which the capture handler sees after the local backends.
func (r *stateRun) submit(t *testing.T, rws blocks.ReadWriteSet) blocks.Transaction {
	t.Helper()
	inv := r.s.newInvocation(t, [][]byte{[]byte("invoke")})
	var responses []*peer.ProposalResponse
	for _, b := range r.s.builders {
		resp, err := b.Endorse(inv, endorsement.Success(rws, "", nil, nil))
		if err != nil {
			t.Fatalf("Endorse: %v", err)
		}
		responses = append(responses, resp)
	}
	if err := r.s.submitter.Submit(t.Context(), sdk.Endorsement{Proposal: inv.Proposal, Responses: responses}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if tx := r.s.capture.findTx(inv.TxID); tx != nil {
			return *tx
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for tx %s", inv.TxID)
	return blocks.Transaction{}
}

// checkLatest checks every key of the case on a fresh snapshot of every backend.
func (r *stateRun) checkLatest(t *testing.T, stepIdx int, want kv) {
	t.Helper()
	for _, b := range r.backends {
		snap, err := b.kvs.Snapshot(t.Context())
		if err != nil {
			t.Fatalf("%s: Snapshot: %v", b.name, err)
		}
		r.checkSnapshot(t, fmt.Sprintf("step %d: %s", stepIdx, b.name), snap, want)
		if err := snap.Close(); err != nil {
			t.Fatalf("%s: close snapshot: %v", b.name, err)
		}
	}
}

// snapshotWant is the expected state of a case's keys as of a block.
type snapshotWant struct {
	block uint64
	want  kv
}

// checkHistory reads every earlier step back through SnapshotAt. A backend
// without history may refuse with state.ErrHistoryUnavailable.
func (r *stateRun) checkHistory(t *testing.T, history []snapshotWant) {
	t.Helper()
	for _, b := range r.backends {
		for i, h := range history {
			snap, err := b.kvs.SnapshotAt(t.Context(), h.block)
			if errors.Is(err, state.ErrHistoryUnavailable) {
				continue
			}
			if err != nil {
				t.Fatalf("%s: SnapshotAt(%d): %v", b.name, h.block, err)
			}
			r.checkSnapshot(t, fmt.Sprintf("history step %d: %s", i, b.name), snap, h.want)
			if err := snap.Close(); err != nil {
				t.Fatalf("%s: close snapshot: %v", b.name, err)
			}
		}
	}
}

func (r *stateRun) checkSnapshot(t *testing.T, where string, snap state.Snapshot, want kv) {
	t.Helper()
	for _, key := range r.keys {
		rec, err := snap.Get(r.s.namespace, r.prefix+key)
		if err != nil {
			t.Fatalf("%s: Get(%s): %v", where, key, err)
		}
		e, exists := want[key]
		switch {
		case !exists && rec != nil:
			t.Errorf("%s: %s = %q at version %d, want absent", where, key, rec.Value, rec.Version)
		case !exists:
		case rec == nil:
			t.Errorf("%s: %s is absent, want %s", where, key, e)
		case (rec.Value == nil) != e.null || string(rec.Value) != e.value || rec.Version != e.version:
			got := entry{value: string(rec.Value), null: rec.Value == nil, version: rec.Version}
			t.Errorf("%s: %s = %s, want %s", where, key, got, e)
		}
	}
}

func (e entry) String() string {
	if e.null {
		return fmt.Sprintf("NULL at version %d", e.version)
	}
	return fmt.Sprintf("%q at version %d", e.value, e.version)
}
