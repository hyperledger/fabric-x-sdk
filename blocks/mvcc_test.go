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

// mockDB is a simple in-memory RecordGetter for tests.
type mockDB map[string]*WriteRecord

func (m mockDB) Get(namespace, key string) (*WriteRecord, error) {
	rec, ok := m[namespace+"/"+key]
	if !ok {
		return nil, nil
	}
	return rec, nil
}

type errDB struct{}

func (errDB) Get(_, _ string) (*WriteRecord, error) {
	return nil, errors.New("db error")
}

func newValidator(db RecordGetter, monotonic bool) *MVCCValidator {
	return NewMVCCValidator(db, monotonic, sdk.NoOpLogger{})
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
		name       string
		db         RecordGetter
		monotonic  bool
		reads      []KVRead
		wantStatus Status
		wantErr    bool
	}{
		{
			name:       "no reads always valid",
			db:         mockDB{},
			reads:      nil,
			wantStatus: StatusCommitted,
		},
		{
			name:       "key absent, version nil: ok",
			db:         mockDB{},
			reads:      []KVRead{{Key: "k", Version: nil}},
			wantStatus: StatusCommitted,
		},
		{
			name: "key absent, version expected: conflict",
			db:   mockDB{},
			reads: []KVRead{
				{Key: "k", Version: &Version{BlockNum: 1, TxNum: 0}},
			},
			wantStatus: StatusMVCCConflict,
		},
		{
			name: "key present, version nil: conflict (fabric mode)",
			db: mockDB{
				ns + "/k": {BlockNum: 1, TxNum: 0},
			},
			monotonic:  false,
			reads:      []KVRead{{Key: "k", Version: nil}},
			wantStatus: StatusMVCCConflict,
		},
		{
			// Matches validate_reads_ns_*'s SQL: no protocol-specific leniency.
			name: "key present, version nil: conflict (fabric-x mode too)",
			db: mockDB{
				ns + "/k": {BlockNum: 1, TxNum: 0},
			},
			monotonic:  true,
			reads:      []KVRead{{Key: "k", Version: nil}},
			wantStatus: StatusMVCCConflict,
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
			wantStatus: StatusCommitted,
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
			wantStatus: StatusMVCCConflict,
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
			wantStatus: StatusCommitted,
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
			wantStatus: StatusMVCCConflict,
		},
		{
			name:       "db error returns unknown status and error",
			db:         errDB{},
			reads:      []KVRead{{Key: "k", Version: &Version{BlockNum: 1}}},
			wantStatus: StatusUnknown,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := newValidator(tc.db, tc.monotonic)
			block := singleTxBlock(ns, 10, tc.reads)
			_, err := v.Validate(block)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			status := block.Transactions[0].Status
			if status != tc.wantStatus {
				t.Errorf("status: got %v, want %v", status, tc.wantStatus)
			}
			if valid := block.Transactions[0].Valid(); valid != tc.wantStatus.Valid() {
				t.Errorf("valid: got %v, want %v", valid, tc.wantStatus.Valid())
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

			if _, err := v.Validate(block); err != nil {
				t.Fatalf("Validate: unexpected error: %v", err)
			}

			if status := block.Transactions[0].Status; status != StatusCommitted {
				t.Errorf("tx0: expected committed, got %v", status)
			}
			if status := block.Transactions[1].Status; status != StatusMVCCConflict {
				t.Errorf("tx1: expected conflict, got %v", status)
			}
			if status := block.Transactions[2].Status; status != StatusCommitted {
				t.Errorf("tx2: expected committed for unrelated key, got %v", status)
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
			if _, err := v.Validate(block2); err != nil {
				t.Fatalf("second Validate: unexpected error: %v", err)
			}
			if status := block2.Transactions[0].Status; status != StatusCommitted {
				t.Errorf("after second Validate: expected committed, got %v", status)
			}
		})
	}
}

// TestMVCCValidatorCodesAndTxFilter verifies that Validate's txFilter and each
// Transaction's RawCode/Reason are driven by the configured Codes map, and that an
// unmapped status (including a nil Codes map) falls back to UncustomizedCode without
// colliding with any real ledger code.
func TestMVCCValidatorCodesAndTxFilter(t *testing.T) {
	const ns = "ns"
	codes := map[Status]int32{
		StatusCommitted:    1,
		StatusMVCCConflict: 11,
		StatusUnknown:      99,
	}

	t.Run("Codes populated", func(t *testing.T) {
		v := newValidator(mockDB{}, false)
		v.Codes = codes

		block := &Block{
			Number: 1,
			Transactions: []Transaction{
				{Number: 0, NsRWS: []NsReadWriteSet{{Namespace: ns, RWS: ReadWriteSet{Reads: []KVRead{{Key: "k", Version: nil}}}}}},
				{Number: 1, NsRWS: []NsReadWriteSet{{Namespace: ns, RWS: ReadWriteSet{Reads: []KVRead{{Key: "missing", Version: &Version{BlockNum: 1}}}}}}},
			},
		}
		txFilter, err := v.Validate(block)
		if err != nil {
			t.Fatalf("Validate: unexpected error: %v", err)
		}

		if got, want := txFilter[0], byte(codes[StatusCommitted]); got != want {
			t.Errorf("txFilter[0]: got %d, want %d", got, want)
		}
		if got, want := block.Transactions[0].RawCode, codes[StatusCommitted]; got != want {
			t.Errorf("tx0 RawCode: got %d, want %d", got, want)
		}

		if got, want := txFilter[1], byte(codes[StatusMVCCConflict]); got != want {
			t.Errorf("txFilter[1]: got %d, want %d", got, want)
		}
		if got, want := block.Transactions[1].RawCode, codes[StatusMVCCConflict]; got != want {
			t.Errorf("tx1 RawCode: got %d, want %d", got, want)
		}
		if reason := block.Transactions[1].Reason; reason == "" {
			t.Error("tx1 Reason: expected a non-empty conflict detail, got empty string")
		}
	})

	t.Run("Codes nil falls back to UncustomizedCode, no collision with a real code", func(t *testing.T) {
		v := newValidator(mockDB{ns + "/k": {BlockNum: 1, TxNum: 0}}, false)

		block := singleTxBlock(ns, 1, []KVRead{{Key: "k", Version: nil}}) // conflict: version nil, record exists
		txFilter, err := v.Validate(block)
		if err != nil {
			t.Fatalf("Validate: unexpected error: %v", err)
		}

		if got, want := block.Transactions[0].RawCode, UncustomizedCode; got != want {
			t.Errorf("RawCode: got %d, want %d", got, want)
		}
		if got, want := txFilter[0], byte(UncustomizedCode); got != want {
			t.Errorf("txFilter[0]: got %d, want %d", got, want)
		}
		// 255 is classic Fabric's INVALID_OTHER_REASON; UncustomizedCode must not
		// truncate to it or any other real single-byte protocol code.
		if txFilter[0] == 255 {
			t.Error("txFilter[0] collides with a real Fabric TxValidationCode byte")
		}
	})

	t.Run("db error uses codeFor(StatusUnknown)", func(t *testing.T) {
		v := newValidator(errDB{}, false)
		v.Codes = codes

		block := singleTxBlock(ns, 1, []KVRead{{Key: "k", Version: &Version{BlockNum: 1}}})
		txFilter, err := v.Validate(block)
		if err == nil {
			t.Fatal("Validate: expected error, got nil")
		}
		if got, want := block.Transactions[0].RawCode, codes[StatusUnknown]; got != want {
			t.Errorf("RawCode: got %d, want %d", got, want)
		}
		if got, want := txFilter[0], byte(codes[StatusUnknown]); got != want {
			t.Errorf("txFilter[0]: got %d, want %d", got, want)
		}
	})
}

// TestMVCCValidatorCrossBlockStaleRead: a key written for the first time in
// block 1 must invalidate a transaction that read it as absent (Version: nil)
// and is only validated in block 2.
func TestMVCCValidatorCrossBlockStaleRead(t *testing.T) {
	const ns = "ns"

	for _, monotonic := range []bool{false, true} {
		name := "fabric"
		if monotonic {
			name = "fabric-x"
		}
		t.Run(name, func(t *testing.T) {
			db := mockDB{}
			v := newValidator(db, monotonic)

			// Block 1: tx0 blind-writes "balance" for the first time.
			block1 := &Block{
				Number: 1,
				Transactions: []Transaction{
					{
						Number: 0,
						NsRWS: []NsReadWriteSet{{
							Namespace: ns,
							RWS:       ReadWriteSet{Writes: []KVWrite{{Key: "balance", Value: []byte("10")}}},
						}},
					},
				},
			}
			if _, err := v.Validate(block1); err != nil {
				t.Fatalf("Validate block1: %v", err)
			}
			if status := block1.Transactions[0].Status; status != StatusCommitted {
				t.Fatalf("block1 tx0: expected committed, got %v", status)
			}
			// Simulate the ledger committing tx0's write before block 2 is validated.
			db[ns+"/balance"] = &WriteRecord{BlockNum: 1, TxNum: 0, Version: 1}

			// Block 2: tx1 reads "balance" with Version: nil, exactly as recorded
			// when it was endorsed before block 1 committed.
			block2 := &Block{
				Number: 2,
				Transactions: []Transaction{
					{
						Number: 0,
						NsRWS: []NsReadWriteSet{{
							Namespace: ns,
							RWS:       ReadWriteSet{Reads: []KVRead{{Key: "balance", Version: nil}}},
						}},
					},
				},
			}
			if _, err := v.Validate(block2); err != nil {
				t.Fatalf("Validate block2: %v", err)
			}
			if status := block2.Transactions[0].Status; status != StatusMVCCConflict {
				t.Errorf("block2 tx1: expected conflict (stale read of key first written in block1), got %v", status)
			}
		})
	}
}
