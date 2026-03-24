/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset/kvrwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
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

func parseKVRWSet(t *testing.T, resp *peer.ProposalResponse) *kvrwset.KVRWSet {
	t.Helper()
	var txRWSet rwset.TxReadWriteSet
	prpBytes := resp.Payload

	// ProposalResponsePayload contains the chaincode action bytes.
	var prp peer.ProposalResponsePayload
	if err := proto.Unmarshal(prpBytes, &prp); err != nil {
		t.Fatalf("unmarshal ProposalResponsePayload: %v", err)
	}
	var ca peer.ChaincodeAction
	if err := proto.Unmarshal(prp.Extension, &ca); err != nil {
		t.Fatalf("unmarshal ChaincodeAction: %v", err)
	}
	if err := proto.Unmarshal(ca.Results, &txRWSet); err != nil {
		t.Fatalf("unmarshal TxReadWriteSet: %v", err)
	}
	if len(txRWSet.NsRwset) != 1 {
		t.Fatalf("expected 1 namespace, got %d", len(txRWSet.NsRwset))
	}
	var kvRWSet kvrwset.KVRWSet
	if err := proto.Unmarshal(txRWSet.NsRwset[0].Rwset, &kvRWSet); err != nil {
		t.Fatalf("unmarshal KVRWSet: %v", err)
	}
	return &kvRWSet
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

func TestEndorse_SingleWrite(t *testing.T) {
	rws := blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: "a", Value: []byte("va")}},
	}
	kv := parseKVRWSet(t, endorse(t, rws))
	if len(kv.Writes) != 1 || kv.Writes[0].Key != "a" || string(kv.Writes[0].Value) != "va" {
		t.Errorf("unexpected writes: %+v", kv.Writes)
	}
}

func TestEndorse_Delete(t *testing.T) {
	rws := blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: "d", IsDelete: true, Value: []byte("ignored")}},
	}
	kv := parseKVRWSet(t, endorse(t, rws))
	if len(kv.Writes) != 1 || !kv.Writes[0].IsDelete {
		t.Errorf("expected delete write, got %+v", kv.Writes)
	}
}

func TestEndorse_ReadAndWrite(t *testing.T) {
	rws := blocks.ReadWriteSet{
		Reads:  []blocks.KVRead{{Key: "r", Version: &blocks.Version{BlockNum: 3, TxNum: 1}}},
		Writes: []blocks.KVWrite{{Key: "w", Value: []byte("vw")}},
	}
	kv := parseKVRWSet(t, endorse(t, rws))
	if len(kv.Reads) != 1 || kv.Reads[0].Key != "r" || kv.Reads[0].Version.BlockNum != 3 {
		t.Errorf("unexpected reads: %+v", kv.Reads)
	}
	if len(kv.Writes) != 1 || kv.Writes[0].Key != "w" {
		t.Errorf("unexpected writes: %+v", kv.Writes)
	}
}

func TestEndorse_SortedOutput(t *testing.T) {
	rws := blocks.ReadWriteSet{
		Reads: []blocks.KVRead{
			{Key: "z"}, {Key: "a"},
		},
		Writes: []blocks.KVWrite{
			{Key: "y", Value: []byte("vy")},
			{Key: "b", Value: []byte("vb")},
		},
	}
	kv := parseKVRWSet(t, endorse(t, rws))
	if kv.Reads[0].Key != "a" || kv.Reads[1].Key != "z" {
		t.Errorf("reads not sorted: %v %v", kv.Reads[0].Key, kv.Reads[1].Key)
	}
	if kv.Writes[0].Key != "b" || kv.Writes[1].Key != "y" {
		t.Errorf("writes not sorted: %v %v", kv.Writes[0].Key, kv.Writes[1].Key)
	}
}
