/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"google.golang.org/protobuf/proto"
)

// fixedSigner always returns the same bytes for signature and identity.
type fixedSigner struct{}

func (fixedSigner) Sign(_ []byte) ([]byte, error) { return []byte("sig"), nil }
func (fixedSigner) Serialize() ([]byte, error)    { return []byte("identity"), nil }

const testNamespace = "mycc"

func endorse(t *testing.T, rws blocks.ReadWriteSet) *peer.ProposalResponse {
	t.Helper()
	in := endorsement.Invocation{
		TxID:         "txid",
		ProposalHash: []byte("prophash"),
		Args:         [][]byte{},
		Namespace:    testNamespace,
	}
	resp, err := NewEndorsementBuilder(fixedSigner{}).Endorse(in, endorsement.ExecutionResult{RWS: rws})
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}
	return resp
}

func parseTx(t *testing.T, resp *peer.ProposalResponse) *applicationpb.TxNamespace {
	t.Helper()
	var tx applicationpb.Tx
	if err := proto.Unmarshal(resp.Payload, &tx); err != nil {
		t.Fatalf("unmarshal Tx: %v", err)
	}
	if len(tx.Namespaces) != 1 {
		t.Fatalf("expected 1 namespace, got %d", len(tx.Namespaces))
	}
	ns := tx.Namespaces[0]
	if ns.NsId != testNamespace {
		t.Errorf("unexpected namespace: %q", ns.NsId)
	}
	return ns
}

func TestEndorse_SignatureAndIdentity(t *testing.T) {
	resp := endorse(t, blocks.ReadWriteSet{})
	if string(resp.Endorsement.Signature) != "sig" {
		t.Errorf("unexpected signature: %q", resp.Endorsement.Signature)
	}
	if string(resp.Endorsement.Endorser) != "identity" {
		t.Errorf("unexpected identity: %q", resp.Endorsement.Endorser)
	}
	if resp.Payload == nil {
		t.Error("Payload must not be nil")
	}
}

func TestEndorse_BlindWrite(t *testing.T) {
	rws := blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: "a", Value: []byte("va")}},
	}
	ns := parseTx(t, endorse(t, rws))
	if len(ns.BlindWrites) != 1 || !bytes.Equal(ns.BlindWrites[0].Key, []byte("a")) {
		t.Errorf("unexpected blind writes: %+v", ns.BlindWrites)
	}
}

func TestEndorse_ReadWrite(t *testing.T) {
	rws := blocks.ReadWriteSet{
		Reads:  []blocks.KVRead{{Key: "k", Version: &blocks.Version{BlockNum: 5}}},
		Writes: []blocks.KVWrite{{Key: "k", Value: []byte("new")}},
	}
	ns := parseTx(t, endorse(t, rws))
	if len(ns.ReadWrites) != 1 {
		t.Fatalf("expected 1 read-write, got %d", len(ns.ReadWrites))
	}
	rw := ns.ReadWrites[0]
	if !bytes.Equal(rw.Key, []byte("k")) || string(rw.Value) != "new" || rw.Version == nil || *rw.Version != 5 {
		t.Errorf("unexpected read-write: %+v", rw)
	}
}

func TestEndorse_Delete(t *testing.T) {
	rws := blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: "d", IsDelete: true, Value: []byte("ignored")}},
	}
	ns := parseTx(t, endorse(t, rws))
	if len(ns.BlindWrites) != 1 || ns.BlindWrites[0].Value != nil {
		t.Errorf("expected nil value for delete, got %+v", ns.BlindWrites)
	}
}

// TestEndorse_NsVersion guards against the regression where Endorse built the
// committed Tx with NsVersion hardcoded to 0, silently discarding whatever
// version the Invocation carried. The committer enforces this as a real MVCC
// staleness check, so a wrong value here would make Endorse's output diverge
// from what the caller built the transaction against.
func TestEndorse_NsVersion(t *testing.T) {
	in := endorsement.Invocation{
		TxID:         "txid",
		ProposalHash: []byte("prophash"),
		Args:         [][]byte{},
		Namespace:    testNamespace,
		NsVersion:    7,
	}
	resp, err := NewEndorsementBuilder(fixedSigner{}).Endorse(in, endorsement.ExecutionResult{})
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}
	ns := parseTx(t, resp)
	if ns.NsVersion != 7 {
		t.Errorf("unexpected NsVersion: got %d, want %d", ns.NsVersion, 7)
	}
}

// TestEndorse_MetadataFixedWidth ensures that the positions don't change.
// [0]=event, [1]=event name, [2]=payload, [3]=arg count, [4:]=args.
// Metadata must always contain exactly 4+len(args) entries, regardless of
// which fields are empty.
func TestEndorse_MetadataFixedWidth(t *testing.T) {
	in := endorsement.Invocation{
		TxID:         "txid",
		ProposalHash: []byte("prophash"),
		Args:         nil,
		Namespace:    testNamespace,
	}
	res := endorsement.ExecutionResult{Event: []byte("myevent"), Payload: []byte("mypayload")}

	resp, err := NewEndorsementBuilder(fixedSigner{}).Endorse(in, res)
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}

	var tx applicationpb.Tx
	if err := proto.Unmarshal(resp.Payload, &tx); err != nil {
		t.Fatalf("unmarshal Tx: %v", err)
	}
	if len(tx.Metadata) != 4 {
		t.Fatalf("expected exactly 4 metadata entries, got %d", len(tx.Metadata))
	}
	if string(tx.Metadata[0]) != "myevent" {
		t.Errorf("expected event %q at metadata[0], got %q", "myevent", tx.Metadata[0])
	}
	// no EventName set, so the default applies, matching the Fabric builder
	if string(tx.Metadata[1]) != endorsement.DefaultEventName {
		t.Errorf("expected event name %q at metadata[1], got %q", endorsement.DefaultEventName, tx.Metadata[1])
	}
	if string(tx.Metadata[2]) != "mypayload" {
		t.Errorf("expected payload %q at metadata[2], got %q", "mypayload", tx.Metadata[2])
	}
	if len(tx.Metadata[3]) != 1 || tx.Metadata[3][0] != 0 {
		t.Errorf("expected arg count 0 at metadata[3], got %v", tx.Metadata[3])
	}
}

// TestEndorse_Args guards the count-byte positional layout for a non-empty
// Args: metadata must carry the count at [3] followed by each arg unpacked,
// one per entry, at [4:].
func TestEndorse_Args(t *testing.T) {
	in := endorsement.Invocation{
		TxID:         "txid",
		ProposalHash: []byte("prophash"),
		Args:         [][]byte{[]byte("a"), []byte("b"), []byte("c")},
		Namespace:    testNamespace,
	}

	resp, err := NewEndorsementBuilder(fixedSigner{}).Endorse(in, endorsement.ExecutionResult{})
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}

	var tx applicationpb.Tx
	if err := proto.Unmarshal(resp.Payload, &tx); err != nil {
		t.Fatalf("unmarshal Tx: %v", err)
	}
	if len(tx.Metadata) != 7 {
		t.Fatalf("expected exactly 7 metadata entries (4 + 3 args), got %d", len(tx.Metadata))
	}
	if len(tx.Metadata[1]) != 0 {
		t.Errorf("expected no event name at metadata[1] without an event, got %q", tx.Metadata[1])
	}
	if len(tx.Metadata[3]) != 1 || tx.Metadata[3][0] != 3 {
		t.Fatalf("expected arg count 3 at metadata[3], got %v", tx.Metadata[3])
	}
	for i, want := range in.Args {
		if string(tx.Metadata[4+i]) != string(want) {
			t.Errorf("arg %d: got %q, want %q", i, tx.Metadata[4+i], want)
		}
	}
}

// TestEndorse_TooManyArgs guards the 255-entry ceiling the count byte can
// express: Endorse must error rather than silently truncate.
func TestEndorse_TooManyArgs(t *testing.T) {
	args := make([][]byte, 256)
	for i := range args {
		args[i] = []byte("x")
	}
	in := endorsement.Invocation{
		TxID:         "txid",
		ProposalHash: []byte("prophash"),
		Args:         args,
		Namespace:    testNamespace,
	}

	_, err := NewEndorsementBuilder(fixedSigner{}).Endorse(in, endorsement.ExecutionResult{})
	if err == nil {
		t.Fatal("expected an error for more than 255 args, got nil")
	}
}

func TestBuildTx(t *testing.T) {
	tests := []struct {
		name      string
		rws       blocks.ReadWriteSet
		namespace string
		nsVersion uint64
		txid      []byte

		expectReadsOnly   []expectedRead
		expectReadWrites  []expectedReadWrite
		expectBlindWrites []expectedWrite
	}{
		{
			name: "single blind write",
			rws: blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{
					{Key: "a", Value: []byte("value-a")},
				},
			},
			namespace: "ns1",
			txid:      []byte("tx1"),
			expectBlindWrites: []expectedWrite{
				{Key: "a", Value: []byte("value-a")},
			},
		},
		{
			name: "read then write same key becomes readwrite",
			rws: blocks.ReadWriteSet{
				Reads: []blocks.KVRead{
					{Key: "a", Version: &blocks.Version{BlockNum: 10}},
				},
				Writes: []blocks.KVWrite{
					{Key: "a", Value: []byte("new")},
				},
			},
			namespace: "ns1",
			txid:      []byte("tx2"),
			expectReadWrites: []expectedReadWrite{
				{Key: "a", Value: []byte("new"), BlockNum: ptr(10)},
			},
		},
		{
			name: "read without write becomes read-only",
			rws: blocks.ReadWriteSet{
				Reads: []blocks.KVRead{
					{Key: "a", Version: &blocks.Version{BlockNum: 7}},
				},
			},
			namespace: "ns1",
			txid:      []byte("tx3"),
			expectReadsOnly: []expectedRead{
				{Key: "a", BlockNum: ptr(7)},
			},
		},
		{
			name: "delete write results in nil value",
			rws: blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{
					{Key: "a", IsDelete: true, Value: []byte("ignored")},
				},
			},
			namespace: "ns1",
			txid:      []byte("tx4"),
			expectBlindWrites: []expectedWrite{
				{Key: "a", Value: nil},
			},
		},
		{
			name: "writes are sorted by key",
			rws: blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{
					{Key: "b", Value: []byte("b")},
					{Key: "a", Value: []byte("a")},
				},
			},
			namespace: "ns1",
			txid:      []byte("tx5"),
			expectBlindWrites: []expectedWrite{
				{Key: "a", Value: []byte("a")},
				{Key: "b", Value: []byte("b")},
			},
		},
		{
			name: "results sorted by key",
			rws: blocks.ReadWriteSet{
				Reads: []blocks.KVRead{
					{Key: "d", Version: &blocks.Version{BlockNum: 7}},
					{Key: "c", Version: &blocks.Version{BlockNum: 7}},
					{Key: "f", Version: &blocks.Version{BlockNum: 9}},
					{Key: "e", Version: &blocks.Version{BlockNum: 8}},
				},
				Writes: []blocks.KVWrite{
					{Key: "a", Value: []byte("a")},
					{Key: "b", Value: []byte("b")},
					{Key: "e", Value: []byte("e")},
					{Key: "f", Value: []byte("f")},
				},
			},
			namespace: "ns1",
			txid:      []byte("tx5"),
			expectReadsOnly: []expectedRead{
				{Key: "c", BlockNum: ptr(7)},
				{Key: "d", BlockNum: ptr(7)},
			},
			expectBlindWrites: []expectedWrite{
				{Key: "a", Value: []byte("a")},
				{Key: "b", Value: []byte("b")},
			},
			expectReadWrites: []expectedReadWrite{
				{Key: "e", Value: []byte("e"), BlockNum: ptr(8)},
				{Key: "f", Value: []byte("f"), BlockNum: ptr(9)},
			},
		},
		{
			// Regression: NsVersion was hardcoded to 0 in buildTx, silently
			// discarding whatever version the caller built the tx against.
			name: "nsVersion is carried through, not hardcoded to zero",
			rws: blocks.ReadWriteSet{
				Writes: []blocks.KVWrite{
					{Key: "a", Value: []byte("value-a")},
				},
			},
			namespace: "ns1",
			nsVersion: 7,
			txid:      []byte("tx6"),
			expectBlindWrites: []expectedWrite{
				{Key: "a", Value: []byte("value-a")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx := buildTx(tt.rws, tt.namespace, tt.nsVersion, nil)
			if len(tx.Namespaces) != 1 {
				t.Fatalf("expected 1 namespace, got %d", len(tx.Namespaces))
			}

			ns := tx.Namespaces[0]
			if ns.NsId != tt.namespace {
				t.Fatalf("unexpected namespace id: got %q, want %q", ns.NsId, tt.namespace)
			}
			if ns.NsVersion != tt.nsVersion {
				t.Errorf("unexpected NsVersion: got %d, want %d", ns.NsVersion, tt.nsVersion)
			}

			assertReadsOnly(t, tt.expectReadsOnly, ns.ReadsOnly)
			assertReadWrites(t, tt.expectReadWrites, ns.ReadWrites)
			assertWrites(t, tt.expectBlindWrites, ns.BlindWrites)
		})
	}
}

type expectedRead struct {
	Key      string
	BlockNum *uint64
}

type expectedWrite struct {
	Key   string
	Value []byte
}
type expectedReadWrite struct {
	Key      string
	BlockNum *uint64
	Value    []byte
}

func assertReadsOnly(t *testing.T, exp []expectedRead, got []*applicationpb.Read) {
	t.Helper()

	if len(got) != len(exp) {
		t.Fatalf("reads-only length mismatch: got %d, want %d", len(got), len(exp))
	}

	for i := range exp {
		if !bytes.Equal(got[i].Key, []byte(exp[i].Key)) {
			t.Errorf("read[%d] key mismatch: got %q, want %q",
				i, got[i].Key, exp[i].Key)
		}

		if exp[i].BlockNum == nil {
			if got[i].Version != nil {
				t.Errorf("read[%d] version: expected nil, got %v", i, *got[i].Version)
			}
		} else {
			if got[i].Version == nil {
				t.Errorf("read[%d] version: expected %d, got nil", i, *exp[i].BlockNum)
			}
			if *got[i].Version != *exp[i].BlockNum {
				t.Errorf("read[%d] version mismatch: got %d, want %d",
					i, *got[i].Version, *exp[i].BlockNum)
			}
		}
	}
}

func assertWrites(t *testing.T, exp []expectedWrite, got []*applicationpb.Write) {
	t.Helper()

	if len(got) != len(exp) {
		t.Fatalf("writes length mismatch: got %d, want %d", len(got), len(exp))
	}

	for i := range exp {
		if !bytes.Equal(got[i].GetKey(), []byte(exp[i].Key)) {
			t.Errorf("write[%d] key mismatch: got %q, want %q",
				i, got[i].GetKey(), exp[i].Key)
		}
		if !bytes.Equal(got[i].GetValue(), exp[i].Value) {
			t.Errorf("write[%d] value mismatch: got %v, want %v",
				i, got[i].GetValue(), exp[i].Value)
		}
	}
}

func assertReadWrites(t *testing.T, exp []expectedReadWrite, got []*applicationpb.ReadWrite) {
	t.Helper()

	if len(got) != len(exp) {
		t.Fatalf("writes length mismatch: got %d, want %d", len(got), len(exp))
	}

	for i := range exp {
		if !bytes.Equal(got[i].GetKey(), []byte(exp[i].Key)) {
			t.Errorf("readwrite[%d] key mismatch: got %q, want %q",
				i, got[i].GetKey(), exp[i].Key)
		}
		if !bytes.Equal(got[i].GetValue(), exp[i].Value) {
			t.Errorf("readwrite[%d] value mismatch: got %v, want %v",
				i, got[i].GetValue(), exp[i].Value)
		}
		if exp[i].BlockNum == nil {
			if got[i].Version != nil {
				t.Errorf("read[%d] version: expected nil, got %v", i, *got[i].Version)
			}
		} else {
			if got[i].Version == nil {
				t.Errorf("readwrite[%d] version: expected %d, got nil", i, *exp[i].BlockNum)
			}
			if *got[i].Version != *exp[i].BlockNum {
				t.Errorf("readwrite[%d] version mismatch: got %d, want %d",
					i, *got[i].Version, *exp[i].BlockNum)
			}
		}
	}
}

func ptr(v uint64) *uint64 {
	return &v
}
