/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"google.golang.org/protobuf/proto"
)

// fixedSigner always returns the same bytes for signature and identity.
type fixedSigner struct{}

func (fixedSigner) Sign(_ []byte) ([]byte, error) { return []byte("sig"), nil }
func (fixedSigner) Serialize() ([]byte, error)    { return []byte("identity"), nil }

var ccID = &peer.ChaincodeID{Name: "mycc"}

func endorse(t *testing.T, rws blocks.ReadWriteSet) *peer.ProposalResponse {
	t.Helper()
	in := endorsement.Invocation{
		TxID:         "txid",
		ProposalHash: []byte("prophash"),
		Args:         [][]byte{},
		CCID:         ccID,
	}
	resp, err := NewEndorsementBuilder(fixedSigner{}).Endorse(in, endorsement.ExecutionResult{RWS: rws})
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}
	return resp
}

func parseTx(t *testing.T, resp *peer.ProposalResponse) *protoblocktx.TxNamespace {
	t.Helper()
	var tx protoblocktx.Tx
	if err := proto.Unmarshal(resp.Payload, &tx); err != nil {
		t.Fatalf("unmarshal Tx: %v", err)
	}
	if len(tx.Namespaces) != 1 {
		t.Fatalf("expected 1 namespace, got %d", len(tx.Namespaces))
	}
	ns := tx.Namespaces[0]
	if ns.NsId != ccID.Name {
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

func TestMarshalRWSet(t *testing.T) {
	tests := []struct {
		name      string
		rws       blocks.ReadWriteSet
		namespace string
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := marshalRWSet(tt.rws, tt.namespace)
			if err != nil {
				t.Fatalf("marshalRWSet returned error: %v", err)
			}

			var tx protoblocktx.Tx
			if err := proto.Unmarshal(raw, &tx); err != nil {
				t.Fatalf("failed to unmarshal tx: %v", err)
			}
			if len(tx.Namespaces) != 1 {
				t.Fatalf("expected 1 namespace, got %d", len(tx.Namespaces))
			}

			ns := tx.Namespaces[0]
			if ns.NsId != tt.namespace {
				t.Fatalf("unexpected namespace id: got %q, want %q", ns.NsId, tt.namespace)
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

func assertReadsOnly(t *testing.T, exp []expectedRead, got []*protoblocktx.Read) {
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

func assertWrites(t *testing.T, exp []expectedWrite, got []*protoblocktx.Write) {
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

func assertReadWrites(t *testing.T, exp []expectedReadWrite, got []*protoblocktx.ReadWrite) {
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
