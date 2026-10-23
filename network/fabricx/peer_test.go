/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

func TestConvertBlockEvent_FieldMapping(t *testing.T) {
	protoBatch := &committerpb.BlockEvent{
		Header: &common.BlockHeader{Number: 42, PreviousHash: []byte("parent-41")},
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

	got := convertBlockEvent(protoBatch)

	if got.BlockNumber != 42 {
		t.Errorf("BlockNumber: want 42, got %d", got.BlockNumber)
	}
	// The hash must be the header hash, the same value the delivery path derives.
	if want := protoutil.BlockHeaderHash(protoBatch.Header); !bytes.Equal(got.Hash, want) {
		t.Errorf("Hash: want %x, got %x", want, got.Hash)
	}
	if !bytes.Equal(got.ParentHash, []byte("parent-41")) {
		t.Errorf("ParentHash: want %q, got %q", "parent-41", got.ParentHash)
	}
	if len(got.Events) != 2 {
		t.Fatalf("want 2 events, got %d", len(got.Events))
	}

	first := got.Events[0]
	if first.ID != "txABC" || first.BlockNum != 42 || first.Number != 3 {
		t.Errorf("first event ref mismatch: %+v", first)
	}
	if first.Status != blocks.StatusCommitted || first.Reason != "COMMITTED" ||
		first.RawCode != int32(committerpb.Status_COMMITTED) {
		t.Errorf("first event status: want COMMITTED, got %v (%q, code %d)", first.Status, first.Reason, first.RawCode)
	}

	second := got.Events[1]
	if second.ID != "txDEF" || second.Number != 7 || second.Status != blocks.StatusMVCCConflict ||
		second.Reason != "ABORTED_MVCC_CONFLICT" || second.RawCode != int32(committerpb.Status_ABORTED_MVCC_CONFLICT) {
		t.Errorf("second event mismatch: %+v", second)
	}
}

func TestConvertBlockEvent_EmptyEvents(t *testing.T) {
	got := convertBlockEvent(&committerpb.BlockEvent{Header: &common.BlockHeader{Number: 7}})
	if got.BlockNumber != 7 {
		t.Errorf("BlockNumber: want 7, got %d", got.BlockNumber)
	}
	if len(got.Events) != 0 {
		t.Errorf("expected empty events slice, got %d", len(got.Events))
	}
}

// A header is always sent by the committer, but a missing one must leave the hashes
// empty rather than hash a nil header into a plausible-looking value.
func TestConvertBlockEvent_NoHeaderLeavesHashesEmpty(t *testing.T) {
	got := convertBlockEvent(&committerpb.BlockEvent{})
	if got.BlockNumber != 0 {
		t.Errorf("BlockNumber: want 0, got %d", got.BlockNumber)
	}
	if got.Hash != nil || got.ParentHash != nil {
		t.Errorf("want nil hashes, got Hash=%x ParentHash=%x", got.Hash, got.ParentHash)
	}
}

// The genesis block has no previous hash; ParentHash must be empty, not a hash of nothing.
func TestConvertBlockEvent_GenesisHasEmptyParentHash(t *testing.T) {
	got := convertBlockEvent(&committerpb.BlockEvent{Header: &common.BlockHeader{Number: 0}})
	if len(got.ParentHash) != 0 {
		t.Errorf("ParentHash: want empty for genesis, got %x", got.ParentHash)
	}
	if len(got.Hash) == 0 {
		t.Error("Hash: want the genesis header hash, got empty")
	}
}

func TestToProtoFilterStatus(t *testing.T) {
	tests := []struct {
		name string
		in   []blocks.Status
		want []committerpb.Status
	}{
		{
			name: "nil in means nil out",
			in:   nil,
			want: nil,
		},
		{
			name: "committed",
			in:   []blocks.Status{blocks.StatusCommitted},
			want: []committerpb.Status{committerpb.Status_COMMITTED},
		},
		{
			name: "endorsement policy failure has no distinct Fabric-X code: expands to the signature-invalid code",
			in:   []blocks.Status{blocks.StatusEndorsementPolicyFailure},
			want: []committerpb.Status{committerpb.Status_ABORTED_SIGNATURE_INVALID},
		},
		{
			name: "invalid signature and endorsement policy failure collapse onto the same code",
			in:   []blocks.Status{blocks.StatusInvalidSignature, blocks.StatusEndorsementPolicyFailure},
			want: []committerpb.Status{committerpb.Status_ABORTED_SIGNATURE_INVALID, committerpb.Status_ABORTED_SIGNATURE_INVALID},
		},
		{
			name: "unrecognized has no fixed code and is dropped",
			in:   []blocks.Status{blocks.StatusUnrecognized},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := toProtoFilterStatus(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("toProtoFilterStatus(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("toProtoFilterStatus(%v)[%d] = %v, want %v", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
