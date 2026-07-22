/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification

// Status is the protocol-neutral outcome of a transaction. It abstracts over the
// finer-grained status codes reported by a specific ordering/commit service (for
// example the Fabric-X sidecar). The original, service-specific code is preserved
// as a human-readable string in the Reason field of TxStatusEvent and
// CommittedTxEvent, so no diagnostic detail is lost.
//
// The concrete mapping from a service status onto a Status lives at the network
// boundary (see network/fabricx), keeping this package free of protocol protos.
type Status int32

const (
	// StatusUnknown means the outcome is not yet known: the transaction has not
	// been validated yet, the finality subscription timed out, or the underlying
	// service reported a code this SDK does not recognise. It is the zero value.
	StatusUnknown Status = iota
	// StatusCommitted means the transaction was committed and its state applied.
	StatusCommitted
	// StatusInvalid means the transaction was well-formed but failed validation,
	// for example an invalid endorsement signature or a read-write (MVCC) conflict.
	StatusInvalid
	// StatusRejected means the transaction was rejected before validation, for
	// example a duplicate transaction ID or a malformed envelope.
	StatusRejected
)

// IsFinal reports whether the status is a terminal outcome (committed or a
// definitive failure) rather than "not yet known". Only StatusUnknown is
// non-final.
func (s Status) IsFinal() bool {
	return s != StatusUnknown
}

// String returns a stable, upper-case label for the status.
func (s Status) String() string {
	switch s {
	case StatusCommitted:
		return "COMMITTED"
	case StatusInvalid:
		return "INVALID"
	case StatusRejected:
		return "REJECTED"
	default:
		return "UNKNOWN"
	}
}
