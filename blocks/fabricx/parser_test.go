/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/protobuf/proto"
)

// buildEnvelope constructs a minimal Envelope whose payload contains a
// protoblocktx.Tx with the given namespaces.
func buildEnvelope(t *testing.T, txID string, tx *protoblocktx.Tx) *common.Envelope {
	t.Helper()
	chdrBytes, err := proto.Marshal(&common.ChannelHeader{TxId: txID, Type: int32(common.HeaderType_MESSAGE)})
	if err != nil {
		t.Fatalf("marshal ChannelHeader: %v", err)
	}
	txBytes, err := proto.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal Tx: %v", err)
	}
	payloadBytes, err := proto.Marshal(&common.Payload{
		Header: &common.Header{ChannelHeader: chdrBytes},
		Data:   txBytes,
	})
	if err != nil {
		t.Fatalf("marshal Payload: %v", err)
	}
	return &common.Envelope{Payload: payloadBytes}
}

func TestParse_BlindWrite(t *testing.T) {
	tx := &protoblocktx.Tx{
		Namespaces: []*protoblocktx.TxNamespace{
			{
				NsId: "ns",
				BlindWrites: []*protoblocktx.Write{
					{Key: []byte("k"), Value: []byte("v")},
				},
			},
		},
	}
	env := buildEnvelope(t, "txid1", tx)

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
	if len(btx.NsRWS[0].RWS.Reads) != 0 {
		t.Errorf("unexpected reads: %+v", rws.Reads)
	}
}

func TestParse_ReadWrite(t *testing.T) {
	version := uint64(7)
	tx := &protoblocktx.Tx{
		Namespaces: []*protoblocktx.TxNamespace{
			{
				NsId: "ns",
				ReadWrites: []*protoblocktx.ReadWrite{
					{Key: []byte("k"), Value: []byte("new"), Version: &version},
				},
			},
		},
	}
	env := buildEnvelope(t, "txid2", tx)
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

func TestParse_ReadWriteZeroVersion(t *testing.T) {
	// Version 0 is a valid MVCC constraint meaning "the key was first written at block 0".
	// It must be preserved in the read, not discarded. Only nil version means "no constraint".
	zero := uint64(0)
	tx := &protoblocktx.Tx{
		Namespaces: []*protoblocktx.TxNamespace{
			{
				NsId: "ns",
				ReadWrites: []*protoblocktx.ReadWrite{
					{Key: []byte("k"), Value: []byte("v"), Version: &zero},
				},
			},
		},
	}
	env := buildEnvelope(t, "txid4", tx)

	p := NewBlockParser(sdk.NoOpLogger{})

	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Reads) != 1 || rws.Reads[0].Version == nil || rws.Reads[0].Version.BlockNum != 0 {
		t.Errorf("expected version {BlockNum:0}, got %+v", rws.Reads[0].Version)
	}
}
