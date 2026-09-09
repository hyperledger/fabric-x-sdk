/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// StatusFromValidationCode maps a classic-Fabric TxValidationCode onto the
// protocol-neutral blocks.Status, alongside the original code's number and
// name so no diagnostic detail is lost even for codes this SDK doesn't
// explicitly name. Codes with a clear counterpart in the generic,
// committer-oriented buckets reuse those buckets directly; ENDORSEMENT_POLICY_FAILURE
// gets its own bucket since it is common and has no committer equivalent to
// fold into; every other code falls back to StatusMalformed with the exact
// code preserved via rawCode/reason.
func StatusFromValidationCode(code peer.TxValidationCode) (status blocks.Status, rawCode int32, reason string) {
	switch code {
	case peer.TxValidationCode_VALID:
		status = blocks.StatusCommitted
	case peer.TxValidationCode_BAD_CREATOR_SIGNATURE:
		status = blocks.StatusInvalidSignature
	case peer.TxValidationCode_DUPLICATE_TXID:
		status = blocks.StatusDuplicateTxID
	case peer.TxValidationCode_MVCC_READ_CONFLICT, peer.TxValidationCode_PHANTOM_READ_CONFLICT:
		status = blocks.StatusMVCCConflict
	case peer.TxValidationCode_ENDORSEMENT_POLICY_FAILURE:
		status = blocks.StatusEndorsementPolicyFailure
	case peer.TxValidationCode_NOT_VALIDATED:
		status = blocks.StatusUnknown
	default:
		status = blocks.StatusMalformed
	}
	return status, int32(code), code.String()
}

// CodesByStatus is the reverse of StatusFromValidationCode. Where we map several
// Fabric codes to the same blocks.Status, we always return the smallest.
func CodesByStatus() map[blocks.Status]int32 {
	return blocks.CodesByStatus(peer.TxValidationCode_name, StatusFromValidationCode)
}
