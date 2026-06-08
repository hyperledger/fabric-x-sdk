/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"testing"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
)

func TestConvertTxEventBatch_FieldMapping(t *testing.T) {
	protoBatch := &committerpb.TxEventBatch{
		BlockNumber: 42,
		Events: []*committerpb.TxEvent{
			{
				Ref:    &committerpb.TxRef{TxId: "txABC", BlockNum: 42, TxNum: 3},
				Status: committerpb.Status_COMMITTED,
			},
			{
				Ref:    &committerpb.TxRef{TxId: "txDEF", BlockNum: 42, TxNum: 7},
				Status: committerpb.Status_ABORTED_MVCC_CONFLICT,
			},
		},
	}

	got := convertTxEventBatch(protoBatch)

	if got.BlockNumber != 42 {
		t.Errorf("BlockNumber: want 42, got %d", got.BlockNumber)
	}
	if len(got.Events) != 2 {
		t.Fatalf("want 2 events, got %d", len(got.Events))
	}

	first := got.Events[0]
	if first.TxID != "txABC" || first.BlockNum != 42 || first.TxNum != 3 {
		t.Errorf("first event ref mismatch: %+v", first)
	}
	if first.Status != committerpb.Status_COMMITTED {
		t.Errorf("first event status: want COMMITTED, got %v", first.Status)
	}

	second := got.Events[1]
	if second.TxID != "txDEF" || second.TxNum != 7 || second.Status != committerpb.Status_ABORTED_MVCC_CONFLICT {
		t.Errorf("second event mismatch: %+v", second)
	}
}

func TestConvertTxEventBatch_EmptyEvents(t *testing.T) {
	got := convertTxEventBatch(&committerpb.TxEventBatch{BlockNumber: 7})
	if got.BlockNumber != 7 {
		t.Errorf("BlockNumber: want 7, got %d", got.BlockNumber)
	}
	if len(got.Events) != 0 {
		t.Errorf("expected empty events slice, got %d", len(got.Events))
	}
}
