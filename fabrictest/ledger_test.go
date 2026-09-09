/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"context"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/committerpb"

	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks/fabricx"
	"github.com/hyperledger/fabric-x-sdk/state"
)

// newTestLedger builds a fabric-x ledger backed by an in-memory database.
func newTestLedger(t *testing.T) *ledger {
	t.Helper()
	db, err := state.NewWriteDB("mychannel", ":memory:")
	if err != nil {
		t.Fatalf("create db: %v", err)
	}
	logger := sdk.NewStdLogger("fabrictest")
	l := newLedger(db, fabricx.NewBlockParser(logger), fabricx.NewMVCCValidator(db.CurrentRecordGetter(), logger))
	t.Cleanup(l.close)
	return l
}

// TestProcess_TxFilterAlignsWithBlockPositions pins the mapping between the
// validator's txFilter (indexed by position in the parsed, compacted transaction
// list) and the block's TRANSACTIONS_FILTER (indexed by position in the block).
// A config transaction is dropped during parsing, so a wholesale copy would shift
// every status after it by one.
func TestProcess_TxFilterAlignsWithBlockPositions(t *testing.T) {
	l := newTestLedger(t)

	envs := []*common.Envelope{
		buildEnvelope(t, "tx0", common.HeaderType_MESSAGE, simpleTx("ns")),
		buildEnvelope(t, "cfg", common.HeaderType_CONFIG, simpleTx("ns")),
		buildEnvelope(t, "tx2", common.HeaderType_MESSAGE, simpleTx("ns")),
	}

	if err := l.process(context.Background(), envs); err != nil {
		t.Fatalf("process: %v", err)
	}

	blk := l.blocks[len(l.blocks)-1]
	filter := blk.Metadata.Metadata[common.BlockMetadataIndex_TRANSACTIONS_FILTER]
	if len(filter) != len(envs) {
		t.Fatalf("txFilter length = %d, want %d", len(filter), len(envs))
	}

	// Positions 0 and 2 hold real transactions and must be committed. Position 1 is
	// the config transaction, which is never validated and keeps the zero status.
	for _, pos := range []int{0, 2} {
		if got := committerpb.Status(filter[pos]); got != committerpb.Status_COMMITTED {
			t.Errorf("txFilter[%d] = %v, want %v", pos, got, committerpb.Status_COMMITTED)
		}
	}
	if got := committerpb.Status(filter[1]); got != committerpb.Status_STATUS_UNSPECIFIED {
		t.Errorf("txFilter[1] (config tx) = %v, want %v", got, committerpb.Status_STATUS_UNSPECIFIED)
	}

	// The event stream reads statuses by block position, so it must agree.
	batch := buildTxEventBatch(blk, &committerpb.StreamAllRequest{})
	if batch == nil {
		t.Fatal("buildTxEventBatch returned nil, want a batch")
	}
	want := map[uint32]string{0: "tx0", 2: "tx2"}
	if len(batch.Events) != len(want) {
		t.Fatalf("got %d events, want %d", len(batch.Events), len(want))
	}
	for _, ev := range batch.Events {
		id, ok := want[ev.Ref.TxNum]
		if !ok {
			t.Errorf("unexpected event at TxNum %d", ev.Ref.TxNum)
			continue
		}
		if ev.Ref.TxId != id {
			t.Errorf("TxNum %d has TxId %q, want %q", ev.Ref.TxNum, ev.Ref.TxId, id)
		}
		if ev.Status != committerpb.Status_COMMITTED {
			t.Errorf("TxNum %d has status %v, want %v", ev.Ref.TxNum, ev.Status, committerpb.Status_COMMITTED)
		}
	}
}
