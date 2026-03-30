/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package blocks

import (
	"errors"
	"testing"

	sdk "github.com/hyperledger/fabric-x-sdk"
)

const (
	statusValid    = byte(0)
	statusConflict = byte(1)
	statusInvalid  = byte(2)
)

// mockDB is a simple in-memory RecordGetter for tests.
type mockDB map[string]*WriteRecord

func (m mockDB) Get(namespace, key string, _ uint64) (*WriteRecord, error) {
	rec, ok := m[namespace+"/"+key]
	if !ok {
		return nil, nil
	}
	return rec, nil
}

type errDB struct{}

func (errDB) Get(_, _ string, _ uint64) (*WriteRecord, error) {
	return nil, errors.New("db error")
}

func newValidator(db RecordGetter, monotonic bool) *MVCCValidator {
	return NewMVCCValidator(db, statusValid, statusConflict, statusInvalid, monotonic, sdk.NoOpLogger{})
}

// singleTxBlock builds a one-transaction Block with the given reads in namespace ns.
func singleTxBlock(ns string, blockNum uint64, reads []KVRead) *Block {
	return &Block{
		Number: blockNum,
		Transactions: []Transaction{
			{
				Number: 0,
				NsRWS:  []NsReadWriteSet{{Namespace: ns, RWS: ReadWriteSet{Reads: reads}}},
			},
		},
	}
}

func TestMVCCValidator(t *testing.T) {
	const ns = "ns"

	tests := []struct {
		name      string
		db        RecordGetter
		monotonic bool
		reads     []KVRead
		wantValid bool
		wantByte  byte
		wantErr   bool
	}{
		{
			name:      "no reads always valid",
			db:        mockDB{},
			reads:     nil,
			wantValid: true,
			wantByte:  statusValid,
		},
		{
			name:      "key absent, version nil: ok",
			db:        mockDB{},
			reads:     []KVRead{{Key: "k", Version: nil}},
			wantValid: true,
			wantByte:  statusValid,
		},
		{
			name: "key absent, version expected: conflict",
			db:   mockDB{},
			reads: []KVRead{
				{Key: "k", Version: &Version{BlockNum: 1, TxNum: 0}},
			},
			wantValid: false,
			wantByte:  statusConflict,
		},
		{
			name: "key present, version nil: conflict (fabric mode)",
			db: mockDB{
				ns + "/k": {BlockNum: 1, TxNum: 0},
			},
			monotonic: false,
			reads:     []KVRead{{Key: "k", Version: nil}},
			wantValid: false,
			wantByte:  statusConflict,
		},
		{
			name: "key present, version nil: blind write ok (fabric-x mode)",
			db: mockDB{
				ns + "/k": {BlockNum: 1, TxNum: 0},
			},
			monotonic: true,
			reads:     []KVRead{{Key: "k", Version: nil}},
			wantValid: true,
			wantByte:  statusValid,
		},
		{
			name: "fabric mode: matching version valid",
			db: mockDB{
				ns + "/k": {BlockNum: 2, TxNum: 1},
			},
			monotonic: false,
			reads: []KVRead{
				{Key: "k", Version: &Version{BlockNum: 2, TxNum: 1}},
			},
			wantValid: true,
			wantByte:  statusValid,
		},
		{
			name: "fabric mode: version mismatch conflict",
			db: mockDB{
				ns + "/k": {BlockNum: 2, TxNum: 1},
			},
			monotonic: false,
			reads: []KVRead{
				{Key: "k", Version: &Version{BlockNum: 2, TxNum: 0}},
			},
			wantValid: false,
			wantByte:  statusConflict,
		},
		{
			name: "fabric-x mode: matching version valid",
			db: mockDB{
				ns + "/k": {Version: 5},
			},
			monotonic: true,
			reads: []KVRead{
				{Key: "k", Version: &Version{BlockNum: 5}},
			},
			wantValid: true,
			wantByte:  statusValid,
		},
		{
			name: "fabric-x mode: version mismatch conflict",
			db: mockDB{
				ns + "/k": {Version: 5},
			},
			monotonic: true,
			reads: []KVRead{
				{Key: "k", Version: &Version{BlockNum: 4}},
			},
			wantValid: false,
			wantByte:  statusConflict,
		},
		{
			name:      "db error returns invalid status and error",
			db:        errDB{},
			reads:     []KVRead{{Key: "k", Version: &Version{BlockNum: 1}}},
			wantValid: false,
			wantByte:  statusInvalid,
			wantErr:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newValidator(tc.db, tc.monotonic)
			block := singleTxBlock(ns, 10, tc.reads)
			txFilter, err := v.Validate(block)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			valid := block.Transactions[0].Valid
			status := txFilter[0]
			if valid != tc.wantValid {
				t.Errorf("valid: got %v, want %v", valid, tc.wantValid)
			}
			if status != tc.wantByte {
				t.Errorf("status: got %v, want %v", status, tc.wantByte)
			}
		})
	}
}

// TestMVCCValidatorIntraBlock verifies that a valid transaction's writes are
// tracked so that a later transaction in the same block that reads the same key
// is detected as a conflict (first writer wins).
func TestMVCCValidatorIntraBlock(t *testing.T) {
	const ns = "ns"

	for _, monotonic := range []bool{false, true} {
		name := "fabric"
		if monotonic {
			name = "fabric-x"
		}
		t.Run(name, func(t *testing.T) {
			v := newValidator(mockDB{}, monotonic)

			// Block with three transactions:
			//   tx0: blind write of "k" (no reads) — always valid
			//   tx1: reads "k" with any version — must conflict because tx0 wrote it
			//   tx2: reads "other" — must not be affected
			block := &Block{
				Number: 1,
				Transactions: []Transaction{
					{
						Number: 0,
						NsRWS: []NsReadWriteSet{{
							Namespace: ns,
							RWS: ReadWriteSet{
								Writes: []KVWrite{{Key: "k", Value: []byte("v1")}},
							},
						}},
					},
					{
						Number: 1,
						NsRWS: []NsReadWriteSet{{
							Namespace: ns,
							RWS:       ReadWriteSet{Reads: []KVRead{{Key: "k", Version: nil}}},
						}},
					},
					{
						Number: 2,
						NsRWS: []NsReadWriteSet{{
							Namespace: ns,
							RWS:       ReadWriteSet{Reads: []KVRead{{Key: "other", Version: nil}}},
						}},
					},
				},
			}

			txFilter, err := v.Validate(block)
			if err != nil {
				t.Fatalf("Validate: unexpected error: %v", err)
			}

			if !block.Transactions[0].Valid || txFilter[0] != statusValid {
				t.Errorf("tx0: expected valid, got valid=%v status=%v", block.Transactions[0].Valid, txFilter[0])
			}
			if block.Transactions[1].Valid || txFilter[1] != statusConflict {
				t.Errorf("tx1: expected conflict, got valid=%v status=%v", block.Transactions[1].Valid, txFilter[1])
			}
			if !block.Transactions[2].Valid || txFilter[2] != statusValid {
				t.Errorf("tx2: expected valid for unrelated key, got valid=%v status=%v", block.Transactions[2].Valid, txFilter[2])
			}

			// A second Validate call resets state; tx1's read of "k" is valid again.
			block2 := &Block{
				Number: 2,
				Transactions: []Transaction{
					{
						Number: 0,
						NsRWS: []NsReadWriteSet{{
							Namespace: ns,
							RWS:       ReadWriteSet{Reads: []KVRead{{Key: "k", Version: nil}}},
						}},
					},
				},
			}
			txFilter2, err := v.Validate(block2)
			if err != nil {
				t.Fatalf("second Validate: unexpected error: %v", err)
			}
			if !block2.Transactions[0].Valid || txFilter2[0] != statusValid {
				t.Errorf("after second Validate: expected valid, got valid=%v status=%v", block2.Transactions[0].Valid, txFilter2[0])
			}
		})
	}
}
