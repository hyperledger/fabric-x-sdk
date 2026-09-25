/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset/kvrwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
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
	return buildEnvelopeNs(t, txID, &rwset.NsReadWriteSet{Namespace: namespace, Rwset: mustMarshal(t, kvs)})
}

// buildEnvelopeNs is buildEnvelope for a transaction that touches several namespaces.
func buildEnvelopeNs(t *testing.T, txID string, namespaces ...*rwset.NsReadWriteSet) *common.Envelope {
	t.Helper()

	ccActionBytes := mustMarshal(t, &peer.ChaincodeAction{
		Results: mustMarshal(t, &rwset.TxReadWriteSet{NsRwset: namespaces}),
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

func buildBlock(t *testing.T, blockNum uint64, envelopes []*common.Envelope, txFilter []byte) *common.Block {
	t.Helper()
	data := make([][]byte, len(envelopes))
	for i, env := range envelopes {
		data[i] = mustMarshal(t, env)
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
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{{Key: "k", Value: []byte("v")}},
	}
	env0 := buildEnvelope(t, "tx0", "ns", kvs)
	env1 := buildEnvelope(t, "tx1", "ns", kvs)
	env2 := buildEnvelope(t, "tx2", "ns", kvs)

	txFilter := []byte{
		byte(peer.TxValidationCode_VALID),
		byte(peer.TxValidationCode_MVCC_READ_CONFLICT),
		byte(peer.TxValidationCode_VALID),
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
	for i, tx := range block.Transactions {
		if tx.Number != int64(i) {
			t.Errorf("tx[%d].Number = %d, want %d", i, tx.Number, i)
		}
	}
}

func TestParse_BasicWrite(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{
			{Key: "k", Value: []byte("v")},
		},
	}
	env := buildEnvelope(t, "txid1", "ns", kvs)

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
}

func TestParse_Delete(t *testing.T) {
	kvs := &kvrwset.KVRWSet{
		Writes: []*kvrwset.KVWrite{
			{Key: "d", IsDelete: true},
		},
	}
	env := buildEnvelope(t, "txid2", "ns", kvs)

	p := NewBlockParser(sdk.NoOpLogger{}, nil)
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

// nsWrite returns the rwset entry of namespace with a write of key "k-<namespace>".
func nsWrite(t *testing.T, namespace string) *rwset.NsReadWriteSet {
	t.Helper()
	kvs := &kvrwset.KVRWSet{Writes: []*kvrwset.KVWrite{{Key: "k-" + namespace, Value: []byte("v")}}}
	return &rwset.NsReadWriteSet{Namespace: namespace, Rwset: mustMarshal(t, kvs)}
}

// buildEnvelopeWithNamespaces builds an envelope with a write in each of the given namespaces.
func buildEnvelopeWithNamespaces(t *testing.T, txID string, names ...string) *common.Envelope {
	t.Helper()
	nss := make([]*rwset.NsReadWriteSet, len(names))
	for i, name := range names {
		nss[i] = nsWrite(t, name)
	}
	return buildEnvelopeNs(t, txID, nss...)
}

// buildConfigEnvelope builds a minimal config transaction envelope.
func buildConfigEnvelope(t *testing.T, txID string) *common.Envelope {
	t.Helper()
	return &common.Envelope{Payload: mustMarshal(t, &common.Payload{
		Header: &common.Header{
			ChannelHeader: mustMarshal(t, &common.ChannelHeader{TxId: txID, Type: int32(common.HeaderType_CONFIG)}),
		},
	})}
}

// allValid returns a txFilter that marks n transactions as valid.
func allValid(n int) []byte {
	txFilter := make([]byte, n)
	for i := range txFilter {
		txFilter[i] = byte(peer.TxValidationCode_VALID)
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
		buildEnvelopeWithNamespaces(t, "tx-foreign", "other"),
		buildEnvelopeWithNamespaces(t, "tx-own", "ns"),
	}, allValid(2))

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
		buildEnvelopeWithNamespaces(t, "tx-mixed", "other", "ns", "other2"),
	}, allValid(1))

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

func TestParse_FilterStripsLifecycleNamespace(t *testing.T) {
	// A chaincode invocation reads its definition from _lifecycle, so the rwset has
	// an entry for it next to the chaincode's own. It is no different from any other
	// foreign namespace.
	lifecycle := &rwset.NsReadWriteSet{
		Namespace: "_lifecycle",
		Rwset: mustMarshal(t, &kvrwset.KVRWSet{
			Reads: []*kvrwset.KVRead{{Key: "namespaces/fields/mycc/Sequence", Version: &kvrwset.Version{BlockNum: 3}}},
		}),
	}
	b := buildBlock(t, 1, []*common.Envelope{
		buildEnvelopeNs(t, "tx0", lifecycle, nsWrite(t, "mycc")),
	}, allValid(1))

	block, err := NewBlockParser(sdk.NoOpLogger{}, []string{"mycc"}).Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the tx to be kept, got %v", txIDs(block))
	}
	nsrws := block.Transactions[0].NsRWS
	if got := namespacesOf(nsrws); !slices.Equal(got, []string{"mycc"}) {
		t.Fatalf("NsRWS namespaces: got %v, want [mycc]", got)
	}
	if w := nsrws[0].RWS.Writes; len(w) != 1 || w[0].Key != "k-mycc" {
		t.Errorf("unexpected writes in the kept namespace: %+v", w)
	}
}

func TestParse_NilFilterChangesNothing(t *testing.T) {
	for name, filter := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			b := buildBlock(t, 1, []*common.Envelope{
				buildEnvelopeWithNamespaces(t, "tx-foreign", "other"),
				buildEnvelopeWithNamespaces(t, "tx-mixed", "_lifecycle", "ns"),
				buildEnvelopeWithNamespaces(t, "tx-own", "ns"),
			}, allValid(3))

			block, err := NewBlockParser(sdk.NoOpLogger{}, filter).Parse(b)
			if err != nil {
				t.Fatal(err)
			}
			if ids := txIDs(block); !slices.Equal(ids, []string{"tx-foreign", "tx-mixed", "tx-own"}) {
				t.Fatalf("transactions: got %v, want all three", ids)
			}
			for i, want := range [][]string{{"other"}, {"_lifecycle", "ns"}, {"ns"}} {
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
				buildConfigEnvelope(t, "cfg"),
				buildEnvelopeWithNamespaces(t, "tx-own", "ns"),
			}, allValid(2))

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
		buildEnvelopeWithNamespaces(t, "tx0", "other"),
		buildEnvelopeWithNamespaces(t, "tx1", "other2"),
	}, allValid(2))
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
		buildEnvelopeWithNamespaces(t, "tx0", "other"),
		buildEnvelopeWithNamespaces(t, "tx1", "ns"),
		buildEnvelopeWithNamespaces(t, "tx2", "other"),
		buildConfigEnvelope(t, "cfg"),
		buildEnvelopeWithNamespaces(t, "tx4", "ns"),
	}, []byte{
		byte(peer.TxValidationCode_VALID),
		byte(peer.TxValidationCode_VALID),
		byte(peer.TxValidationCode_MVCC_READ_CONFLICT),
		byte(peer.TxValidationCode_VALID),
		byte(peer.TxValidationCode_MVCC_READ_CONFLICT),
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
	env := buildEnvelopeWithNamespaces(t, "tx-mixed", "other", "ns")

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
		buildEnvelopeWithNamespaces(t, "tx-foreign", "other"),
		buildEnvelopeWithNamespaces(t, "tx-mixed", "other", "ns1", "ns2"),
		buildEnvelopeWithNamespaces(t, "tx-reversed", "ns2", "ns1"),
		buildEnvelopeWithNamespaces(t, "tx-ns2", "ns2"),
		buildEnvelopeWithNamespaces(t, "tx-unnamed", "", "ns1"), // an entry without a name is never decoded
		buildEnvelopeWithNamespaces(t, "tx-none"),
	}
	b := buildBlock(t, 1, envs, allValid(len(envs)))

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
			{"tx-unnamed", []string{"ns1"}},
			{"tx-none", []string{}},
		}},
		{"one namespace", []string{"ns1"}, []txNamespaces{
			{"tx-mixed", []string{"ns1"}},
			{"tx-reversed", []string{"ns1"}},
			{"tx-unnamed", []string{"ns1"}},
		}},
		{"several namespaces are OR-ed, tx order is kept", []string{"ns1", "ns2"}, []txNamespaces{
			{"tx-mixed", []string{"ns1", "ns2"}},
			{"tx-reversed", []string{"ns2", "ns1"}},
			{"tx-ns2", []string{"ns2"}},
			{"tx-unnamed", []string{"ns1"}},
		}},
		{"namespace that no tx touches", []string{"ns1", "absent"}, []txNamespaces{
			{"tx-mixed", []string{"ns1"}},
			{"tx-reversed", []string{"ns1"}},
			{"tx-unnamed", []string{"ns1"}},
		}},
		{"no match", []string{"absent"}, nil},
		{"an empty name never matches", []string{""}, nil},
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

func TestParse_FilterIgnoresUndecodableForeignNamespace(t *testing.T) {
	// Namespaces that are filtered out are not decoded, so garbage in one of them
	// neither fails the tx nor costs us the namespaces we asked for.
	env := buildEnvelopeNs(t, "tx-mixed",
		&rwset.NsReadWriteSet{Namespace: "other", Rwset: []byte{0xff, 0xff, 0xff}},
		nsWrite(t, "ns"),
	)
	b := buildBlock(t, 1, []*common.Envelope{env}, allValid(1))

	block, err := NewBlockParser(sdk.NoOpLogger{}, []string{"ns"}).Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("expected the tx to be kept, got %v", txIDs(block))
	}
	if got := namespacesOf(block.Transactions[0].NsRWS); !slices.Equal(got, []string{"ns"}) {
		t.Errorf("NsRWS namespaces: got %v, want [ns]", got)
	}

	// decoding everything, the same envelope is undecodable
	if _, err := NewBlockParser(sdk.NoOpLogger{}, nil).ParseTx(env); err == nil {
		t.Error("expected ParseTx to fail on the undecodable namespace")
	}
}
