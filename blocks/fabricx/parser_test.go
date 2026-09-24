/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/protobuf/proto"
)

// buildEnvelope constructs a minimal Envelope whose payload contains a
// applicationpb.Tx with the given namespaces.
func buildEnvelope(t *testing.T, txID string, tx *applicationpb.Tx) *common.Envelope {
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

func buildBlock(t *testing.T, blockNum uint64, envelopes []*common.Envelope, txFilter []byte) *common.Block {
	t.Helper()
	data := make([][]byte, len(envelopes))
	for i, env := range envelopes {
		var err error
		data[i], err = proto.Marshal(env)
		if err != nil {
			t.Fatalf("marshal envelope %d: %v", i, err)
		}
	}
	metadata := make([][]byte, int(common.BlockMetadataIndex_TRANSACTIONS_FILTER)+1)
	metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER] = txFilter
	return &common.Block{
		Header: &common.BlockHeader{Number: blockNum},
		Data:   &common.BlockData{Data: data},
		Metadata: &common.BlockMetadata{
			Metadata: metadata,
		},
	}
}

func TestParse_TxNumber(t *testing.T) {
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{NsId: "ns", BlindWrites: []*applicationpb.Write{{Key: []byte("k"), Value: []byte("v")}}},
		},
	}
	env0 := buildEnvelope(t, "tx0", tx)
	env1 := buildEnvelope(t, "tx1", tx)
	env2 := buildEnvelope(t, "tx2", tx)

	txFilter := []byte{
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_ABORTED_MVCC_CONFLICT),
		byte(committerpb.Status_COMMITTED),
	}
	b := buildBlock(t, 5, []*common.Envelope{env0, env1, env2}, txFilter)

	p := NewBlockParser(sdk.NoOpLogger{})
	block, err := p.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 3 {
		t.Fatalf("expected 3 transactions, got %d", len(block.Transactions))
	}
	for i, btx := range block.Transactions {
		if btx.Number != int64(i) {
			t.Errorf("tx[%d].Number = %d, want %d", i, btx.Number, i)
		}
	}
}

func TestParse_BlindWrite(t *testing.T) {
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{
				NsId: "ns",
				BlindWrites: []*applicationpb.Write{
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
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{
				NsId: "ns",
				ReadWrites: []*applicationpb.ReadWrite{
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

func TestParse_ReadOnly(t *testing.T) {
	// reads_only must surface as a read dependency, same as the read half of ReadWrite.
	version := uint64(3)
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{
				NsId:      "ns",
				ReadsOnly: []*applicationpb.Read{{Key: []byte("k"), Version: &version}},
			},
		},
	}
	env := buildEnvelope(t, "txid-readonly", tx)
	p := NewBlockParser(sdk.NoOpLogger{})

	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 0 {
		t.Errorf("expected no writes for a read-only key, got %+v", rws.Writes)
	}
	if len(rws.Reads) != 1 || rws.Reads[0].Key != "k" || rws.Reads[0].Version == nil || rws.Reads[0].Version.BlockNum != 3 {
		t.Errorf("unexpected reads: %+v", rws.Reads)
	}
}

func TestParse_ReadOnlyNeverWritten(t *testing.T) {
	// nil version (never written) must round-trip too, not just an explicit one.
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{
				NsId:      "ns",
				ReadsOnly: []*applicationpb.Read{{Key: []byte("k")}},
			},
		},
	}
	env := buildEnvelope(t, "txid-readonly-nil", tx)
	p := NewBlockParser(sdk.NoOpLogger{})

	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}
	rws := btx.NsRWS[0].RWS
	if len(rws.Reads) != 1 || rws.Reads[0].Key != "k" || rws.Reads[0].Version != nil {
		t.Errorf("unexpected reads: %+v", rws.Reads)
	}
}

func TestParse_ReadWriteZeroVersion(t *testing.T) {
	// Version 0 is a valid MVCC constraint meaning "the key was first written at block 0".
	// It must be preserved in the read, not discarded. Only nil version means "no constraint".
	zero := uint64(0)
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{
				NsId: "ns",
				ReadWrites: []*applicationpb.ReadWrite{
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

func TestParse_Events(t *testing.T) {
	txID := "txid-event"
	eventPayload := []byte(`{"type":"Transfer"}`)

	tx := &applicationpb.Tx{
		// [0]=event, [1]=event name, [2]=payload (absent), [3]=arg count (zero)
		Metadata: [][]byte{eventPayload, []byte("Transfer"), nil, {0}},
		Namespaces: []*applicationpb.TxNamespace{{
			NsId: "ns",
			BlindWrites: []*applicationpb.Write{
				{Key: []byte("k"), Value: []byte("v")},
			},
		}},
	}
	env := buildEnvelope(t, txID, tx)

	p := NewBlockParser(sdk.NoOpLogger{})
	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}

	if string(btx.Event) != string(eventPayload) {
		t.Errorf("event: got %q, want %q", btx.Event, eventPayload)
	}
	if btx.EventName != "Transfer" {
		t.Errorf("event name: got %q, want %q", btx.EventName, "Transfer")
	}

	// all writes should be in NsRWS (no synthetic writes to strip)
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || rws.Writes[0].Key != "k" {
		t.Errorf("expected only real write in NsRWS, got %+v", rws.Writes)
	}
}

func TestParse_InputArgs(t *testing.T) {
	txID := "txid-input"
	args := [][]byte{[]byte("invoke"), []byte("arg1"), []byte("arg2")}

	// [0]=event, [1]=event name, [2]=payload (all absent), [3]=arg count, [4:]=args
	metadata := append([][]byte{nil, nil, nil, {byte(len(args))}}, args...) //nolint:gosec // len(args) is 3.

	tx := &applicationpb.Tx{
		Metadata: metadata,
		Namespaces: []*applicationpb.TxNamespace{{
			NsId: "ns",
			BlindWrites: []*applicationpb.Write{
				{Key: []byte("k"), Value: []byte("v")},
			},
		}},
	}
	env := buildEnvelope(t, txID, tx)

	p := NewBlockParser(sdk.NoOpLogger{})
	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}

	// input args must be populated
	if len(btx.InputArgs) != len(args) {
		t.Fatalf("InputArgs len: got %d, want %d", len(btx.InputArgs), len(args))
	}
	for i, arg := range args {
		if string(btx.InputArgs[i]) != string(arg) {
			t.Errorf("InputArgs[%d]: got %q, want %q", i, btx.InputArgs[i], arg)
		}
	}

	// all writes should be in NsRWS (no synthetic writes to strip)
	rws := btx.NsRWS[0].RWS
	if len(rws.Writes) != 1 || rws.Writes[0].Key != "k" {
		t.Errorf("expected only real write in NsRWS, got %+v", rws.Writes)
	}
}
