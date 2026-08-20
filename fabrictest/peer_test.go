/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"google.golang.org/protobuf/proto"
)

// buildEnvelope constructs a minimal Fabric-X Envelope whose payload contains tx,
// with the given channel header type (defaults to HeaderType_MESSAGE via headerType).
func buildEnvelope(t *testing.T, txID string, headerType common.HeaderType, tx *applicationpb.Tx) *common.Envelope {
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

// buildTestBlock constructs a common.Block from envelopes and a txFilter (one
// committerpb.Status byte per envelope, matching how fabrictest's ledger stores it).
func buildTestBlock(t *testing.T, blockNum uint64, envelopes []*common.Envelope, txFilter []byte) *common.Block {
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
		Header:   &common.BlockHeader{Number: blockNum},
		Data:     &common.BlockData{Data: data},
		Metadata: &common.BlockMetadata{Metadata: metadata},
	}
}

func simpleTx(nsID string) *applicationpb.Tx {
	return &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{NsId: nsID, BlindWrites: []*applicationpb.Write{{Key: []byte("k"), Value: []byte("v")}}},
		},
	}
}

func TestBuildTxEventBatch_NamespaceFilterExcludesNonMatching(t *testing.T) {
	env := buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, simpleTx("other"))
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{FilterNamespaces: []string{"basic"}})
	if batch != nil {
		t.Fatalf("expected nil batch (no matching namespace), got %+v", batch)
	}
}

func TestBuildTxEventBatch_NamespaceFilterIncludesMatching(t *testing.T) {
	env := buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, simpleTx("basic"))
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{FilterNamespaces: []string{"basic"}})
	if batch == nil || len(batch.Events) != 1 {
		t.Fatalf("expected 1 event, got %+v", batch)
	}
	if batch.Events[0].Ref.TxId != "tx0" {
		t.Errorf("TxId: got %q, want tx0", batch.Events[0].Ref.TxId)
	}
}

func TestBuildTxEventBatch_NoFilterIncludesAll(t *testing.T) {
	env := buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, simpleTx("anything"))
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{})
	if batch == nil || len(batch.Events) != 1 {
		t.Fatalf("expected 1 event with an empty filter, got %+v", batch)
	}
}

func TestBuildTxEventBatch_PartialNamespaceMatchOnlyIncludesMatchingNamespaces(t *testing.T) {
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{NsId: "ns1", BlindWrites: []*applicationpb.Write{{Key: []byte("k1"), Value: []byte("v1")}}},
			{NsId: "ns2", BlindWrites: []*applicationpb.Write{{Key: []byte("k2"), Value: []byte("v2")}}},
		},
	}
	env := buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, tx)
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{
		FilterNamespaces:     []string{"ns2"},
		IncludeReadWriteSets: true,
	})
	if batch == nil || len(batch.Events) != 1 {
		t.Fatalf("expected 1 event, got %+v", batch)
	}
	ns := batch.Events[0].Namespaces
	if len(ns) != 1 || ns[0].NsId != "ns2" {
		t.Errorf("expected only ns2 in Namespaces (proto: only matching namespaces are included), got %+v", ns)
	}
}

func TestBuildTxEventBatch_StatusFilter(t *testing.T) {
	committed := buildEnvelope(t, "tx-committed", common.HeaderType_MESSAGE, simpleTx("basic"))
	aborted := buildEnvelope(t, "tx-aborted", common.HeaderType_MESSAGE, simpleTx("basic"))
	block := buildTestBlock(t, 1, []*common.Envelope{committed, aborted}, []byte{
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_ABORTED_MVCC_CONFLICT),
	})

	// No filter: both included, each with its own status.
	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{})
	if batch == nil || len(batch.Events) != 2 {
		t.Fatalf("expected 2 events, got %+v", batch)
	}
	if batch.Events[0].Status != committerpb.Status_COMMITTED {
		t.Errorf("Events[0].Status: got %v, want COMMITTED", batch.Events[0].Status)
	}
	if batch.Events[1].Status != committerpb.Status_ABORTED_MVCC_CONFLICT {
		t.Errorf("Events[1].Status: got %v, want ABORTED_MVCC_CONFLICT", batch.Events[1].Status)
	}

	// Filtered to COMMITTED only.
	batch = buildTxEventBatch(block, &committerpb.StreamAllRequest{FilterStatus: []committerpb.Status{committerpb.Status_COMMITTED}})
	if batch == nil || len(batch.Events) != 1 || batch.Events[0].Ref.TxId != "tx-committed" {
		t.Fatalf("expected only the committed tx, got %+v", batch)
	}

	// Filtered to ABORTED_MVCC_CONFLICT only.
	batch = buildTxEventBatch(block, &committerpb.StreamAllRequest{FilterStatus: []committerpb.Status{committerpb.Status_ABORTED_MVCC_CONFLICT}})
	if batch == nil || len(batch.Events) != 1 || batch.Events[0].Ref.TxId != "tx-aborted" {
		t.Fatalf("expected only the aborted tx, got %+v", batch)
	}

	// Filtered to a status neither tx has: no events at all.
	batch = buildTxEventBatch(block, &committerpb.StreamAllRequest{FilterStatus: []committerpb.Status{committerpb.Status_MALFORMED_BAD_ENVELOPE}})
	if batch != nil {
		t.Fatalf("expected nil batch, got %+v", batch)
	}
}

func TestBuildTxEventBatch_IncludeFlagsDefaultToEmpty(t *testing.T) {
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{NsId: "basic", BlindWrites: []*applicationpb.Write{{Key: []byte("k"), Value: []byte("v")}}},
		},
		Endorsements: []*applicationpb.Endorsements{
			{EndorsementsWithIdentity: []*applicationpb.EndorsementWithIdentity{{Endorsement: []byte("sig")}}},
		},
		Metadata: [][]byte{[]byte("input"), []byte("events")},
	}
	env := buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, tx)
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	// All include_* flags left at their zero value (false).
	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{})
	if batch == nil || len(batch.Events) != 1 {
		t.Fatalf("expected 1 event, got %+v", batch)
	}
	e := batch.Events[0]
	if len(e.Namespaces) != 0 {
		t.Errorf("Namespaces should be empty when include_read_write_sets is false, got %+v", e.Namespaces)
	}
	if len(e.Endorsements) != 0 {
		t.Errorf("Endorsements should be empty when include_endorsements is false, got %+v", e.Endorsements)
	}
	if len(e.Metadata) != 0 {
		t.Errorf("Metadata should be empty when include_metadata is false, got %+v", e.Metadata)
	}
}

func TestBuildTxEventBatch_IncludeEndorsementsAndMetadata(t *testing.T) {
	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{
			{NsId: "basic", BlindWrites: []*applicationpb.Write{{Key: []byte("k"), Value: []byte("v")}}},
		},
		Endorsements: []*applicationpb.Endorsements{
			{EndorsementsWithIdentity: []*applicationpb.EndorsementWithIdentity{{Endorsement: []byte("sig")}}},
		},
		Metadata: [][]byte{[]byte("input"), []byte("events")},
	}
	env := buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, tx)
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{
		IncludeEndorsements: true,
		IncludeMetadata:     true,
	})
	if batch == nil || len(batch.Events) != 1 {
		t.Fatalf("expected 1 event, got %+v", batch)
	}
	e := batch.Events[0]
	if len(e.Endorsements) != 1 || len(e.Endorsements[0].EndorsementsWithIdentity) != 1 ||
		string(e.Endorsements[0].EndorsementsWithIdentity[0].Endorsement) != "sig" {
		t.Errorf("unexpected Endorsements: %+v", e.Endorsements)
	}
	if len(e.Metadata) != 2 || string(e.Metadata[0]) != "input" || string(e.Metadata[1]) != "events" {
		t.Errorf("unexpected Metadata: %+v", e.Metadata)
	}
}

func TestBuildTxEventBatch_ConfigTxSkipped(t *testing.T) {
	env := buildEnvelope(t, "tx0", common.HeaderType_CONFIG, simpleTx("basic"))
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{})
	if batch != nil {
		t.Fatalf("expected nil batch (config tx must be skipped), got %+v", batch)
	}
}

func TestBuildTxEventBatch_ClassicFabricTxSkipped(t *testing.T) {
	// A "fabric" (classic) network's transactions use HeaderType_ENDORSER_TRANSACTION
	// and a wire format that isn't applicationpb.Tx; the notifier must not misinterpret
	// them, and simply skips them rather than emitting garbage.
	env := buildEnvelope(t, "tx0", common.HeaderType_ENDORSER_TRANSACTION, simpleTx("basic"))
	block := buildTestBlock(t, 1, []*common.Envelope{env}, []byte{byte(committerpb.Status_COMMITTED)})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{})
	if batch != nil {
		t.Fatalf("expected nil batch (classic fabric tx must be skipped), got %+v", batch)
	}
}

func TestBuildTxEventBatch_MalformedEnvelopeSkipped(t *testing.T) {
	good := buildEnvelope(t, "tx-good", common.HeaderType_MESSAGE, simpleTx("basic"))
	bad := &common.Envelope{Payload: []byte("not a valid protobuf payload \xff\xff")}
	block := buildTestBlock(t, 1, []*common.Envelope{good, bad}, []byte{
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_COMMITTED),
	})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{})
	if batch == nil || len(batch.Events) != 1 || batch.Events[0].Ref.TxId != "tx-good" {
		t.Fatalf("expected only the well-formed tx to survive, got %+v", batch)
	}
}

func TestBuildTxEventBatch_RefFieldsAndTxNumOrdering(t *testing.T) {
	env0 := buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, simpleTx("basic"))
	env1 := buildEnvelope(t, "tx1", common.HeaderType_MESSAGE, simpleTx("basic"))
	block := buildTestBlock(t, 42, []*common.Envelope{env0, env1}, []byte{
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_COMMITTED),
	})

	batch := buildTxEventBatch(block, &committerpb.StreamAllRequest{})
	if batch == nil || len(batch.Events) != 2 {
		t.Fatalf("expected 2 events, got %+v", batch)
	}
	if batch.BlockNumber != 42 {
		t.Errorf("BlockNumber: got %d, want 42", batch.BlockNumber)
	}
	for i, want := range []string{"tx0", "tx1"} {
		ref := batch.Events[i].Ref
		if ref.TxId != want {
			t.Errorf("Events[%d].Ref.TxId: got %q, want %q", i, ref.TxId, want)
		}
		if ref.TxNum != uint32(i) {
			t.Errorf("Events[%d].Ref.TxNum: got %d, want %d", i, ref.TxNum, i)
		}
		if ref.BlockNum != 42 {
			t.Errorf("Events[%d].Ref.BlockNum: got %d, want 42", i, ref.BlockNum)
		}
	}
}

func TestStatusMatches(t *testing.T) {
	tests := []struct {
		name   string
		status committerpb.Status
		filter []committerpb.Status
		want   bool
	}{
		{"empty filter matches everything", committerpb.Status_COMMITTED, nil, true},
		{"matching filter", committerpb.Status_COMMITTED, []committerpb.Status{committerpb.Status_COMMITTED}, true},
		{"non-matching filter", committerpb.Status_COMMITTED, []committerpb.Status{committerpb.Status_ABORTED_MVCC_CONFLICT}, false},
		{"OR match within filter", committerpb.Status_ABORTED_MVCC_CONFLICT, []committerpb.Status{committerpb.Status_COMMITTED, committerpb.Status_ABORTED_MVCC_CONFLICT}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusMatches(tc.status, tc.filter); got != tc.want {
				t.Errorf("statusMatches(%v, %v) = %v, want %v", tc.status, tc.filter, got, tc.want)
			}
		})
	}
}

func TestFilterNamespaces(t *testing.T) {
	all := []*applicationpb.TxNamespace{{NsId: "ns1"}, {NsId: "ns2"}}

	matching, touched := filterNamespaces(all, nil)
	if !touched || len(matching) != 2 {
		t.Errorf("empty filter: expected all namespaces to match, got matching=%+v touched=%v", matching, touched)
	}

	matching, touched = filterNamespaces(all, []string{"ns2"})
	if !touched || len(matching) != 1 || matching[0].NsId != "ns2" {
		t.Errorf("expected only ns2 to match, got matching=%+v touched=%v", matching, touched)
	}

	matching, touched = filterNamespaces(all, []string{"nope"})
	if touched || len(matching) != 0 {
		t.Errorf("expected no match, got matching=%+v touched=%v", matching, touched)
	}
}
