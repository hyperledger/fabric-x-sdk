/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset/kvrwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/protobuf/proto"
)

// mustMarshal panics on marshal error (test helper only).
func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal %T: %v", m, err)
	}
	return b
}

// buildEnvelope builds the deeply nested Fabric envelope for a single
// chaincode action with the given namespace KVRWSet.
func buildEnvelope(t *testing.T, txID, namespace string, kvs *kvrwset.KVRWSet) *common.Envelope {
	t.Helper()

	ccActionBytes := mustMarshal(t, &peer.ChaincodeAction{
		Results: mustMarshal(t, &rwset.TxReadWriteSet{
			NsRwset: []*rwset.NsReadWriteSet{
				{Namespace: namespace, Rwset: mustMarshal(t, kvs)},
			},
		}),
	})

	capBytes := mustMarshal(t, &peer.ChaincodeActionPayload{
		Action: &peer.ChaincodeEndorsedAction{
			ProposalResponsePayload: mustMarshal(t, &peer.ProposalResponsePayload{
				Extension: ccActionBytes,
			}),
		},
	})

	txBytes := mustMarshal(t, &peer.Transaction{
		Actions: []*peer.TransactionAction{
			{Payload: capBytes},
		},
	})

	payloadBytes := mustMarshal(t, &common.Payload{
		Header: &common.Header{
			ChannelHeader: mustMarshal(t, &common.ChannelHeader{TxId: txID, Type: int32(common.HeaderType_ENDORSER_TRANSACTION)}),
		},
		Data: txBytes,
	})

	return &common.Envelope{Payload: payloadBytes}
}

func TestParse_BasicWrite(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{
			{Key: "k", Value: []byte("v")},
		},
	}
	env := buildEnvelope(t, "txid1", "ns", kvs)

	p := NewBlockParser(sdk.NoOpLogger{})
	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	if btx.ID != "txid1" {
		t.Errorf("unexpected txID: %q", btx.ID)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || rws.Writes[0].Key != "k" || string(rws.Writes[0].Value) != "v" {
		t.Errorf("unexpected writes: %+v", rws.Writes)
	}
}

func TestParse_Delete(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{
			{Key: "d", IsDelete: true},
		},
	}
	env := buildEnvelope(t, "txid2", "ns", kvs)

	p := NewBlockParser(sdk.NoOpLogger{})
	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || !rws.Writes[0].IsDelete {
		t.Errorf("expected delete write, got %+v", rws.Writes)
	}
}

func TestParse_ReadWrite(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Reads: []*kvrwset.KVRead{
			{Key: "k", Version: &kvrwset.Version{BlockNum: 7}},
		},
		Writes: []*kvrwset.KVWrite{
			{Key: "k", Value: []byte("new")},
		},
	}

	env := buildEnvelope(t, "txid3", "ns", kvs)
	p := NewBlockParser(sdk.NoOpLogger{})

	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || rws.Writes[0].Key != "k" || string(rws.Writes[0].Value) != "new" {
		t.Errorf("unexpected writes: %+v", rws.Writes)
	}
	if len(rws.Reads) != 1 || rws.Reads[0].Key != "k" || rws.Reads[0].Version == nil || rws.Reads[0].Version.BlockNum != 7 {
		t.Errorf("unexpected reads: %+v", rws.Reads)
	}
}
