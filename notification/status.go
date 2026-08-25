/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification

// Status is the protocol-neutral outcome of a transaction. It abstracts over the
// finer-grained status codes reported by a specific ordering/commit service (for
// example the Fabric-X sidecar). The original, service-specific code is preserved
// in full on TxStatusEvent and CommittedTxEvent: RawCode carries the exact code
// and Reason its human-readable name, so no diagnostic detail is lost even though
// Status itself only names the outcomes callers commonly need to branch on.
//
// The concrete mapping from a service status onto a Status lives at the network
// boundary (see network/fabricx), keeping this package free of protocol protos.
type Status int32

const (
	// StatusUnknown means the outcome is not yet known: the transaction has not
	// been validated yet, or the finality subscription timed out. It is the zero
	// value.
	StatusUnknown Status = iota
	// StatusCommitted means the transaction was committed and its state applied.
	StatusCommitted
	// StatusInvalidSignature means the transaction's endorsement signature failed
	// verification.
	StatusInvalidSignature
	// StatusMVCCConflict means the transaction's read set conflicted with a
	// concurrent write (a multi-version concurrency control conflict).
	StatusMVCCConflict
	// StatusDuplicateTxID means the transaction ID had already been submitted.
	StatusDuplicateTxID
	// StatusMalformed means the transaction was rejected before validation
	// because its envelope was structurally invalid; see Reason/RawCode for the
	// specific violation.
	StatusMalformed
	// StatusUnrecognized means the underlying service reported a status code
	// this SDK does not have a name for; see Reason/RawCode for the raw value.
	StatusUnrecognized
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
	default:
		return "UNKNOWN"
	}
}
