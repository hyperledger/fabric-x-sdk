/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package sdk

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
)

// Endorsement is a simple wrapper around the signed proposal that was
// sent to the endorsers and their responses.
type Endorsement struct {
	Proposal  *peer.Proposal
	Responses []*peer.ProposalResponse
}

type Signer interface {
	Sign(msg []byte) ([]byte, error)
	Serialize() ([]byte, error)
}
