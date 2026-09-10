/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package blocks

import "slices"

// Status is the protocol-neutral outcome of a transaction. It abstracts over
// the finer-grained status codes reported by a specific ledger (classic
// Fabric's peer.TxValidationCode, or the Fabric-X sidecar's
// committerpb.Status). The original, ledger-specific code is preserved in
// full alongside Status wherever it is produced: RawCode carries the exact
// code and Reason its human-readable name, so no diagnostic detail is lost
// even though Status itself only names the outcomes callers commonly need to
// branch on.
//
// The concrete mapping from a ledger-specific code onto a Status lives in the
// protocol-specific sub-packages that already depend on the relevant protos
// (blocks/fabric, blocks/fabricx), keeping this package free of them.
type Status int32

const (
	// StatusUnknown means the outcome is not yet known: the transaction has not
	// been validated yet, or a finality subscription timed out. It is the zero
	// value.
	StatusUnknown Status = 0
	// StatusCommitted means the transaction was committed and its state applied.
	StatusCommitted Status = 1
	// StatusInvalidSignature means the transaction's endorsement signature failed
	// verification.
	StatusInvalidSignature Status = 2
	// StatusMVCCConflict means the transaction's read set conflicted with a
	// concurrent write (a multi-version concurrency control conflict).
	StatusMVCCConflict Status = 3
	// StatusDuplicateTxID means the transaction ID had already been submitted.
	StatusDuplicateTxID Status = 4
	// StatusMalformed means the transaction was generically invalid or
	// structurally rejected; see RawCode/Reason for the specific cause.
	StatusMalformed Status = 5
	// StatusUnrecognized means the underlying ledger reported a status code
	// this SDK does not have a name for; see Reason/RawCode for the raw value.
	StatusUnrecognized Status = 6

	// 7-9 are reserved for future generic, cross-protocol statuses.

	// StatusEndorsementPolicyFailure means a classic-Fabric transaction failed
	// its namespace's endorsement policy. The Fabric-X committer has no distinct
	// code for this: it classifies both a bad creator signature and an
	// endorsement policy failure as a signature error.
	StatusEndorsementPolicyFailure Status = 10
)

// IsFinal reports whether the status is a terminal outcome (committed or a
// definitive failure) rather than "not yet known". Only StatusUnknown is
// non-final.
func (s Status) IsFinal() bool {
	return s != StatusUnknown
}

// Valid reports whether the status represents a successful commit.
// StatusCommitted is the only valid status.
func (s Status) Valid() bool {
	return s == StatusCommitted
}

// CodesByStatus inverts a ledger-specific forward-mapping function fn (e.g.
// fabric.StatusFromValidationCode or fabricx.StatusFromCommitterStatus) over every
// code named in names (that protocol's proto-generated *_name map, e.g.
// peer.TxValidationCode_name), returning the smallest raw code that maps to each
// Status. Where several codes map to the same Status, the smallest is returned so
// the result is deterministic regardless of map iteration order.
func CodesByStatus[T ~int32](names map[int32]string, fn func(T) (Status, int32, string)) map[Status]int32 {
	codes := make([]int32, 0, len(names))
	for code := range names {
		codes = append(codes, code)
	}
	slices.Sort(codes)

	result := make(map[Status]int32, len(codes))
	for _, code := range codes {
		status, rawCode, _ := fn(T(code))
		if _, ok := result[status]; !ok {
			result[status] = rawCode
		}
	}
	return result
}

// String returns a stable, upper-case label for the status.
func (s Status) String() string {
	switch s {
	case StatusCommitted:
		return "COMMITTED"
	case StatusInvalidSignature:
		return "INVALID_SIGNATURE"
	case StatusMVCCConflict:
		return "MVCC_CONFLICT"
	case StatusDuplicateTxID:
		return "DUPLICATE_TX_ID"
	case StatusMalformed:
		return "MALFORMED"
	case StatusUnrecognized:
		return "UNRECOGNIZED"
	case StatusEndorsementPolicyFailure:
		return "ENDORSEMENT_POLICY_FAILURE"
	default:
		return "UNKNOWN"
	}
}
