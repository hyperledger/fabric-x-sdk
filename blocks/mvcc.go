/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package blocks

import (
	"fmt"

	sdk "github.com/hyperledger/fabric-x-sdk"
)

// UncustomizedCode is what codeFor returns for a Status with no entry in
// MVCCValidator.Codes (including when Codes is nil): it means "no real ledger-specific
// code was assigned".
const UncustomizedCode int32 = 99

func NewMVCCValidator(db RecordGetter, monotonicVersions bool, logger sdk.Logger) *MVCCValidator {
	return &MVCCValidator{
		db:                db,
		monotonicVersions: monotonicVersions,
		logger:            logger,
	}
}

// RecordGetter retrieves the latest committed state from a state database to validate against.
type RecordGetter interface {
	Get(namespace, key string) (*WriteRecord, error)
}

// MVCCValidator does basic MVCC checks against the latest committed state. Use for sanity checks or testing.
type MVCCValidator struct {
	db RecordGetter

	// Codes maps a Status to the ledger-specific code (e.g. peer.TxValidationCode or
	// committerpb.Status, cast to int32) recorded in Transaction.RawCode — for
	// StatusCommitted, StatusMVCCConflict, and StatusUnknown, the three outcomes
	// checkNs and Validate produce. A caller that emulates a specific ledger's wire
	// format sets these (see blocks/fabric and blocks/fabricx); most callers only care
	// about Transaction.Status and can leave Codes nil — a status with no entry
	// resolves to UncustomizedCode.
	Codes map[Status]int32

	monotonicVersions bool
	logger            sdk.Logger
	// pendingWrites tracks keys written by valid transactions earlier in the current block.
	// A read of any key in this set is an intra-block MVCC conflict.
	pendingWrites map[string]struct{}
}

// codeFor returns the code configured in Codes for status, or UncustomizedCode if
// status has no entry (a nil Codes map included — a read from a nil map is safe and
// always misses).
func (l *MVCCValidator) codeFor(status Status) int32 {
	if code, ok := l.Codes[status]; ok {
		return code
	}
	return UncustomizedCode
}

// Validate validates all transactions in block using MVCC checks.
// It updates each transaction's Status in place and returns a txFilter
// byte slice with one entry per transaction position, where each byte
// indicates the validation status.
// Returns an error (and the partially-filled txFilter) if a database error occurs.
func (l *MVCCValidator) Validate(block *Block) ([]byte, error) {
	l.pendingWrites = make(map[string]struct{})
	txFilter := make([]byte, len(block.Transactions))

	for i := range block.Transactions {
		tx := &block.Transactions[i]
		status := StatusCommitted
		reason := ""
		var vErr error

		for _, rws := range tx.NsRWS {
			s, r, err := l.checkNs(rws.Namespace, rws.RWS.Reads)
			if err != nil {
				status, reason, vErr = s, err.Error(), err
				break
			}
			if s != StatusCommitted {
				status, reason = s, r
				break
			}
		}

		code := l.codeFor(status)
		tx.SetStatus(status, code, reason)
		txFilter[i] = byte(code)

		if vErr != nil {
			return txFilter, vErr
		}

		if status == StatusCommitted {
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
// and any pending intra-block writes. The returned Status is StatusCommitted (valid),
// StatusMVCCConflict, or — only alongside a non-nil error — StatusUnknown. On a
// conflict, the returned string names the offending key and why it conflicted, for
// Transaction.Reason.
func (l *MVCCValidator) checkNs(ns string, reads []KVRead) (Status, string, error) {
	// Intra-block conflict: a valid earlier transaction in this block already wrote the key.
	for _, r := range reads {
		if _, ok := l.pendingWrites[ns+":"+r.Key]; ok {
			return StatusMVCCConflict, fmt.Sprintf(
				"key %q was already written earlier in this block", r.Key), nil
		}
	}

	for _, r := range reads {
		rec, err := l.db.Get(ns, r.Key)
		if err != nil {
			return StatusUnknown, "", fmt.Errorf("failed to get state for key %q: %v", r.Key, err)
		}

		// no existing read at this blockheight
		if rec == nil {
			if r.Version == nil {
				continue // we expected none: ok
			}
			return StatusMVCCConflict, fmt.Sprintf(
				"key %q: expected version %d:%d, but key does not exist",
				r.Key, r.Version.BlockNum, r.Version.TxNum), nil // we expected a specific version
		}

		// there is an existing record...

		// ...but we expected none?
		if r.Version == nil {
			// Conflict in both protocols — matches validate_reads_ns_*'s
			// "actual.key IS NOT NULL AND expected.version IS NULL" case.
			return StatusMVCCConflict, fmt.Sprintf(
				"key %q: expected no existing record, but one exists", r.Key), nil
		}

		// ...and we expected a specific version
		if l.monotonicVersions {
			if rec.Version != r.Version.BlockNum {
				reason := fmt.Sprintf("key %q: read version=%d, blockchain version=%d",
					r.Key, r.Version.BlockNum, rec.Version)
				l.logger.Debugf("RWS INVALID for %s", reason)
				return StatusMVCCConflict, reason, nil // version mismatch
			}
		} else {
			if rec.BlockNum != r.Version.BlockNum || rec.TxNum != r.Version.TxNum {
				reason := fmt.Sprintf("key %q: read version=%d:%d, blockchain version=%d:%d",
					r.Key, r.Version.BlockNum, r.Version.TxNum,
					rec.BlockNum, rec.TxNum)
				l.logger.Debugf("RWS INVALID for %s", reason)
				return StatusMVCCConflict, reason, nil // version mismatch
			}
		}
	}
	// if we made it this far, all reads are valid
	return StatusCommitted, "", nil
}
