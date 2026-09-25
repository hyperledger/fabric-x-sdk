/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"google.golang.org/protobuf/proto"
)

// buildEnvelope constructs a minimal Envelope whose payload contains a
// applicationpb.Tx with the given namespaces.
func buildEnvelope(t *testing.T, txID string, tx *applicationpb.Tx) *common.Envelope {
	t.Helper()
	return buildEnvelopeOfType(t, txID, common.HeaderType_MESSAGE, tx)
}

// buildEnvelopeOfType is buildEnvelope with an explicit channel header type.
func buildEnvelopeOfType(t *testing.T, txID string, headerType common.HeaderType, tx *applicationpb.Tx) *common.Envelope {
	t.Helper()
	chdrBytes, err := proto.Marshal(&common.ChannelHeader{TxId: txID, Type: int32(headerType)})
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

	p := NewBlockParser(sdk.NoOpLogger{}, nil)
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

	p := NewBlockParser(sdk.NoOpLogger{}, nil)

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
	p := NewBlockParser(sdk.NoOpLogger{}, nil)

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
	p := NewBlockParser(sdk.NoOpLogger{}, nil)

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
	p := NewBlockParser(sdk.NoOpLogger{}, nil)

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

	p := NewBlockParser(sdk.NoOpLogger{}, nil)

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
	eventBytes, err := proto.Marshal(&peer.ChaincodeEvent{
		ChaincodeId: "ns",
		TxId:        txID,
		EventName:   "log",
		Payload:     eventPayload,
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}

	tx := &applicationpb.Tx{
		Metadata: [][]byte{nil, eventBytes}, // no input at [0], event at [1]
		Namespaces: []*applicationpb.TxNamespace{{
			NsId: "ns",
			BlindWrites: []*applicationpb.Write{
				{Key: []byte("k"), Value: []byte("v")},
			},
		}},
	}
	env := buildEnvelope(t, txID, tx)

	p := NewBlockParser(sdk.NoOpLogger{}, nil)
	btx, err := p.ParseTx(env)
	if err != nil {
		t.Fatal(err)
	}

	// event bytes must be populated
	if len(btx.Events) == 0 {
		t.Fatal("expected Events to be set")
	}
	evt := &peer.ChaincodeEvent{}
	if err := proto.Unmarshal(btx.Events, evt); err != nil {
		t.Fatalf("unmarshal events: %v", err)
	}
	if string(evt.Payload) != string(eventPayload) {
		t.Errorf("event payload: got %q, want %q", evt.Payload, eventPayload)
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
	inputBytes, err := proto.Marshal(&peer.ChaincodeInput{Args: args})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	tx := &applicationpb.Tx{
		Metadata: [][]byte{inputBytes, nil}, // input at [0], no event at [1]
		Namespaces: []*applicationpb.TxNamespace{{
			NsId: "ns",
			BlindWrites: []*applicationpb.Write{
				{Key: []byte("k"), Value: []byte("v")},
			},
		}},
	}
	env := buildEnvelope(t, txID, tx)

	p := NewBlockParser(sdk.NoOpLogger{}, nil)
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

// txWithNamespaces returns a Tx with a blind write of key "k-<name>" in each of the given namespaces.
func txWithNamespaces(names ...string) *applicationpb.Tx {
	tx := &applicationpb.Tx{}
	for _, name := range names {
		tx.Namespaces = append(tx.Namespaces, &applicationpb.TxNamespace{
			NsId:        name,
			BlindWrites: []*applicationpb.Write{{Key: []byte("k-" + name), Value: []byte("v")}},
		})
	}
	return tx
}

// allCommitted returns a txFilter that marks n transactions as committed.
func allCommitted(n int) []byte {
	txFilter := make([]byte, n)
	for i := range txFilter {
		txFilter[i] = byte(committerpb.Status_COMMITTED)
	}
	return txFilter
}

func txIDs(block blocks.Block) []string {
	ids := make([]string, len(block.Transactions))
	for i, tx := range block.Transactions {
		ids[i] = tx.ID
	}
	return ids
}

func namespacesOf(nsrws []blocks.NsReadWriteSet) []string {
	names := make([]string, len(nsrws))
	for i, ns := range nsrws {
		names[i] = ns.Namespace
	}
	return names
}

func TestParse_FilterDropsForeignTx(t *testing.T) {
	b := buildBlock(t, 1, []*common.Envelope{
		buildEnvelope(t, "tx-foreign", txWithNamespaces("other")),
		buildEnvelope(t, "tx-own", txWithNamespaces("ns")),
	}, allCommitted(2))

	block, err := NewBlockParser(sdk.NoOpLogger{}, []string{"ns"}).Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if ids := txIDs(block); !slices.Equal(ids, []string{"tx-own"}) {
		t.Errorf("transactions: got %v, want [tx-own]", ids)
	}
}

func TestParse_FilterMixedTxKeepsOnlyListedNamespaces(t *testing.T) {
	b := buildBlock(t, 1, []*common.Envelope{
		buildEnvelope(t, "tx-mixed", txWithNamespaces("other", "ns", "other2")),
	}, allCommitted(1))

	block, err := NewBlockParser(sdk.NoOpLogger{}, []string{"ns"}).Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the mixed tx to be kept, got %v", txIDs(block))
	}
	nsrws := block.Transactions[0].NsRWS
	if got := namespacesOf(nsrws); !slices.Equal(got, []string{"ns"}) {
		t.Fatalf("NsRWS namespaces: got %v, want [ns]", got)
	}
	if w := nsrws[0].RWS.Writes; len(w) != 1 || w[0].Key != "k-ns" {
		t.Errorf("unexpected writes in the kept namespace: %+v", w)
	}
}

func TestParse_NilFilterChangesNothing(t *testing.T) {
	for name, filter := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			b := buildBlock(t, 1, []*common.Envelope{
				buildEnvelope(t, "tx-foreign", txWithNamespaces("other")),
				buildEnvelope(t, "tx-mixed", txWithNamespaces("other", "ns")),
				buildEnvelope(t, "tx-own", txWithNamespaces("ns")),
			}, allCommitted(3))

			block, err := NewBlockParser(sdk.NoOpLogger{}, filter).Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if ids := txIDs(block); !slices.Equal(ids, []string{"tx-foreign", "tx-mixed", "tx-own"}) {
				t.Fatalf("transactions: got %v, want all three", ids)
			}
			for i, want := range [][]string{{"other"}, {"other", "ns"}, {"ns"}} {
				if got := namespacesOf(block.Transactions[i].NsRWS); !slices.Equal(got, want) {
					t.Errorf("tx[%d] namespaces: got %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestParse_FilterSkipsConfigTx(t *testing.T) {
	// a config tx is never returned, whether or not a filter is set
	for name, filter := range map[string][]string{"filtered": {"ns"}, "unfiltered": nil} {
		t.Run(name, func(t *testing.T) {
			b := buildBlock(t, 1, []*common.Envelope{
				buildEnvelopeOfType(t, "cfg", common.HeaderType_CONFIG, txWithNamespaces("ns")),
				buildEnvelope(t, "tx-own", txWithNamespaces("ns")),
			}, allCommitted(2))

			block, err := NewBlockParser(sdk.NoOpLogger{}, filter).Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if ids := txIDs(block); !slices.Equal(ids, []string{"tx-own"}) {
				t.Errorf("transactions: got %v, want [tx-own]", ids)
			}
		})
	}
}

func TestParse_FilterAllTxsFilteredOutStillReturnsBlock(t *testing.T) {
	b := buildBlock(t, 9, []*common.Envelope{
		buildEnvelope(t, "tx0", txWithNamespaces("other")),
		buildEnvelope(t, "tx1", txWithNamespaces("other2")),
	}, allCommitted(2))
	b.Header.PreviousHash = []byte("parent")

	block, err := NewBlockParser(sdk.NoOpLogger{}, []string{"ns"}).Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 0 {
		t.Errorf("expected no transactions, got %v", txIDs(block))
	}
	if block.Number != 9 {
		t.Errorf("Number: got %d, want 9", block.Number)
	}
	if want := protoutil.BlockHeaderHash(b.Header); !bytes.Equal(block.Hash, want) {
		t.Errorf("Hash: got %x, want %x", block.Hash, want)
	}
	if !bytes.Equal(block.ParentHash, []byte("parent")) {
		t.Errorf("ParentHash: got %q, want %q", block.ParentHash, "parent")
	}
}

func TestParse_FilterPreservesTxNumber(t *testing.T) {
	// Kept transactions are not renumbered, and each keeps the status of its own
	// position in the block, not of its position among the kept ones.
	b := buildBlock(t, 5, []*common.Envelope{
		buildEnvelope(t, "tx0", txWithNamespaces("other")),
		buildEnvelope(t, "tx1", txWithNamespaces("ns")),
		buildEnvelope(t, "tx2", txWithNamespaces("other")),
		buildEnvelopeOfType(t, "cfg", common.HeaderType_CONFIG, txWithNamespaces("ns")),
		buildEnvelope(t, "tx4", txWithNamespaces("ns")),
	}, []byte{
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_ABORTED_MVCC_CONFLICT),
		byte(committerpb.Status_STATUS_UNSPECIFIED),
		byte(committerpb.Status_ABORTED_MVCC_CONFLICT),
	})

	block, err := NewBlockParser(sdk.NoOpLogger{}, []string{"ns"}).Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if ids := txIDs(block); !slices.Equal(ids, []string{"tx1", "tx4"}) {
		t.Fatalf("transactions: got %v, want [tx1 tx4]", ids)
	}
	for i, want := range []struct {
		number int64
		valid  bool
	}{{1, true}, {4, false}} {
		tx := block.Transactions[i]
		if tx.Number != want.number {
			t.Errorf("%s: Number = %d, want %d", tx.ID, tx.Number, want.number)
		}
		if tx.Valid() != want.valid {
			t.Errorf("%s: Valid() = %v, want %v", tx.ID, tx.Valid(), want.valid)
		}
	}
}

func TestParseTx_AppliesParserNamespaces(t *testing.T) {
	// ParseTx applies the namespaces the parser was created with, the same as Parse does.
	env := buildEnvelope(t, "tx-mixed", txWithNamespaces("other", "ns"))

	for _, tc := range []struct {
		name       string
		namespaces []string
		want       []string
	}{
		{"no namespaces decodes everything", nil, []string{"other", "ns"}},
		{"listed namespaces only", []string{"ns"}, []string{"ns"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			btx, err := NewBlockParser(sdk.NoOpLogger{}, tc.namespaces).ParseTx(env)
			if err != nil {
				t.Fatal(err)
			}
			if got := namespacesOf(btx.NsRWS); !slices.Equal(got, tc.want) {
				t.Errorf("NsRWS namespaces: got %v, want %v", got, tc.want)
			}
		})
	}

	// no listed namespace is touched: no tx, like a config tx, and no error
	btx, err := NewBlockParser(sdk.NoOpLogger{}, []string{"absent"}).ParseTx(env)
	if err != nil || btx != nil {
		t.Errorf("got %+v, %v; want nil, nil", btx, err)
	}
}

func TestParse_FilterNamespaceRule(t *testing.T) {
	// A tx is kept if it has a read/write set in any listed namespace, and then only
	// those are kept, in the order they have in the tx.
	envs := []*common.Envelope{
		buildEnvelope(t, "tx-foreign", txWithNamespaces("other")),
		buildEnvelope(t, "tx-mixed", txWithNamespaces("other", "ns1", "ns2")),
		buildEnvelope(t, "tx-reversed", txWithNamespaces("ns2", "ns1")),
		buildEnvelope(t, "tx-ns2", txWithNamespaces("ns2")),
		buildEnvelope(t, "tx-none", txWithNamespaces()),
	}
	b := buildBlock(t, 1, envs, allCommitted(len(envs)))

	type txNamespaces struct {
		ID         string
		Namespaces []string
	}
	tests := []struct {
		name   string
		filter []string
		want   []txNamespaces
	}{
		{"no filter", nil, []txNamespaces{
			{"tx-foreign", []string{"other"}},
			{"tx-mixed", []string{"other", "ns1", "ns2"}},
			{"tx-reversed", []string{"ns2", "ns1"}},
			{"tx-ns2", []string{"ns2"}},
			{"tx-none", []string{}},
		}},
		{"one namespace", []string{"ns1"}, []txNamespaces{
			{"tx-mixed", []string{"ns1"}},
			{"tx-reversed", []string{"ns1"}},
		}},
		{"several namespaces are OR-ed, tx order is kept", []string{"ns1", "ns2"}, []txNamespaces{
			{"tx-mixed", []string{"ns1", "ns2"}},
			{"tx-reversed", []string{"ns2", "ns1"}},
			{"tx-ns2", []string{"ns2"}},
		}},
		{"namespace that no tx touches", []string{"ns1", "absent"}, []txNamespaces{
			{"tx-mixed", []string{"ns1"}},
			{"tx-reversed", []string{"ns1"}},
		}},
		{"no match", []string{"absent"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			block, err := NewBlockParser(sdk.NoOpLogger{}, tc.filter).Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			var got []txNamespaces
			for _, tx := range block.Transactions {
				got = append(got, txNamespaces{tx.ID, namespacesOf(tx.NsRWS)})
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
