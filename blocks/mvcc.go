/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package blocks

import (
	"fmt"

	sdk "github.com/hyperledger/fabric-x-sdk"
)

func NewMVCCValidator(db RecordGetter, valid, conflict, invalid byte, monotonicVersions bool, logger sdk.Logger) *MVCCValidator {
	return &MVCCValidator{
		db:                db,
		valid:             valid,
		conflict:          conflict,
		invalid:           invalid,
		monotonicVersions: monotonicVersions,
		logger:            logger,
		pendingWrites:     make(map[string]struct{}),
	}
}

// RecordGetter retrieves existing state from a state database to validate against.
type RecordGetter interface {
	Get(namespace, key string, lastBlock uint64) (*WriteRecord, error)
}

// MVCCValidator does basic MVCC checks against a state at a certain block height. Use for sanity checks or testing.
type MVCCValidator struct {
	db                RecordGetter
	valid             byte
	conflict          byte
	invalid           byte
	monotonicVersions bool
	logger            sdk.Logger
	// pendingWrites tracks keys written by valid transactions earlier in the current block.
	// A read of any key in this set is an intra-block MVCC conflict.
	pendingWrites map[string]struct{}
}

// Validate validates all transactions in block using MVCC checks.
// It updates each transaction's Valid field in place and returns a txFilter byte slice
// with one entry per transaction position, where each byte indicates the validation status.
// Returns an error (and the partially-filled txFilter) if a database error occurs.
func (l *MVCCValidator) Validate(block *Block) ([]byte, error) {
	l.pendingWrites = make(map[string]struct{})
	txFilter := make([]byte, len(block.Transactions))

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		txStatus := l.valid
		txValid := true

		for _, rws := range tx.NsRWS {
			valid, status, err := l.checkNs(rws.Namespace, block.Number, rws.RWS.Reads)
			if err != nil {
				tx.Valid = false
				txFilter[i] = l.invalid
				return txFilter, err
			}
			if !valid {
				txValid = false
				txStatus = status
				break
			}
		}

		tx.Valid = txValid
		txFilter[i] = txStatus

		if txValid {
			for _, rws := range tx.NsRWS {
				for _, w := range rws.RWS.Writes {
					l.pendingWrites[rws.Namespace+":"+w.Key] = struct{}{}
				}
			}
		}
	}

	return txFilter, nil
}

// checkNs does MVCC checks for a single namespace's reads against the committed state
// and any pending intra-block writes.
func (l *MVCCValidator) checkNs(ns string, blockNum uint64, reads []KVRead) (bool, byte, error) {
	// Intra-block conflict: a valid earlier transaction in this block already wrote the key.
	for _, r := range reads {
		if _, ok := l.pendingWrites[ns+":"+r.Key]; ok {
			return false, l.conflict, nil
		}
	}

	for _, r := range reads {
		rec, err := l.db.Get(ns, r.Key, blockNum)
		if err != nil {
			return false, l.invalid, fmt.Errorf("failed to get state for key %q: %v", r.Key, err)
		}

		// no existing read at this blockheight
		if rec == nil {
			if r.Version == nil {
				continue // we expected none: ok
			} else {
				return false, l.conflict, nil // we expected a specific version
			}
		}

		// there is an existing record...

		// ...but we expected none?
		if r.Version == nil {
			if l.monotonicVersions {
				// In fabric-x, proto version=0 is dropped by the parser (we expect version > 0),
				// resulting in nil. Nil means no version constraint — treat as blind write.
				continue
			}
			// not ok in fabric, we expected no record to exist
			return false, l.conflict, nil
		}

		// ...and we expected a specific version
		if l.monotonicVersions {
			if rec.Version != r.Version.BlockNum {
				return false, l.conflict, nil // version mismatch
			}
		} else {
			if rec.BlockNum != r.Version.BlockNum || rec.TxNum != r.Version.TxNum {
				l.logger.Debugf("RWS INVALID for key %q: read version=%d:%d, blockchain version=%d:%d",
					r.Key, r.Version.BlockNum, r.Version.TxNum,
					rec.BlockNum, rec.TxNum)
				return false, l.conflict, nil // version mismatch
			}
		}
	}
	// if we made it this far, all reads are valid
	return true, l.valid, nil
}
