/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// StatusFromCommitterStatus maps a Fabric-X sidecar status onto the
// protocol-neutral blocks.Status, alongside the original code's number and
// name so no diagnostic detail is lost even for codes this SDK doesn't
// explicitly name.
func StatusFromCommitterStatus(s committerpb.Status) (status blocks.Status, rawCode int32, reason string) {
	switch s {
	case committerpb.Status_COMMITTED:
		status = blocks.StatusCommitted
	case committerpb.Status_STATUS_UNSPECIFIED:
		status = blocks.StatusUnknown
	case committerpb.Status_ABORTED_SIGNATURE_INVALID:
		// Also covers what classic Fabric reports separately as
		// StatusEndorsementPolicyFailure: the committer doesn't distinguish a
		// bad creator signature from an endorsement policy failure.
		status = blocks.StatusInvalidSignature
	case committerpb.Status_ABORTED_MVCC_CONFLICT:
		status = blocks.StatusMVCCConflict
	case committerpb.Status_REJECTED_DUPLICATE_TX_ID:
		status = blocks.StatusDuplicateTxID
	default:
		if s >= 100 {
			// The MALFORMED_* family (and any future pre-validation rejection
			// code the sidecar adds in this range before the SDK names it).
			status = blocks.StatusMalformed
		} else {
			status = blocks.StatusUnrecognized
		}
	}
	return status, int32(s), s.String()
}

// CodesByStatus is the reverse of StatusFromCommitterStatus. Where we map several
// Fabric-X codes to the same blocks.Status, we always return the smallest.
func CodesByStatus() map[blocks.Status]int32 {
	return blocks.CodesByStatus(committerpb.Status_name, StatusFromCommitterStatus)
}
