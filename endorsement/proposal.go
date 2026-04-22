/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package endorsement can read a Fabric-style SignedProposal (the request made to a
// peer to execute chaincode). It can also create a signed response, with a transaction
// or read/write set style that can be either Fabric- or Fabric-X format.
//
// Fabric-X does not support "traditional" chaincode, but this package makes it possible to
// create signed endorsements of read/write sets following the new programming model.
package endorsement

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// Builder creates the signed ProposalResponse.
type Builder interface {
	Endorse(in Invocation, res ExecutionResult) (*peer.ProposalResponse, error)
}

// Invocation instructs the endorser to execute a transaction.
type Invocation struct {
	TxID     string
	Nonce    []byte
	Creator  []byte
	Args     [][]byte
	CCID     *peer.ChaincodeID
	Channel  string
	Proposal *peer.Proposal
	// ProposalHash is necessary for the endorsement in Fabric 2 and 3
	ProposalHash []byte
}

// ExecutionResult is the outcome of an execution.
type ExecutionResult struct {
	// RWS is the set of reads and writes as a result of the execution.
	RWS blocks.ReadWriteSet
	// Event is the
	Event []byte
	// Status is a code that should follow the HTTP status codes.
	Status int32
	//Message associated with the response code.
	Message string
	// Payload that can be used to include metadata with this response.
	Payload []byte
}

func (res ExecutionResult) Response() *peer.Response {
	return &peer.Response{
		Status:  res.Status,
		Message: res.Message,
		Payload: res.Payload,
	}
}

func BadRequest(msg string) ExecutionResult {
	return ExecutionResult{
		Status:  http.StatusBadRequest,
		Message: http.StatusText(http.StatusBadRequest),
		Payload: []byte(msg),
	}
}

func Success(rws blocks.ReadWriteSet, event []byte, payload []byte) ExecutionResult {
	return ExecutionResult{
		RWS:     rws,
		Event:   event,
		Status:  200,
		Message: "OK",
		Payload: payload,
	}
}

// Parse extracts the fields that are relevant for endorsement from a SignedProposal.
// expectedTime is an optional timestamp of the expected time of signing. If provided,
// validation will fail in case of a larger difference than 5 minutes.
//
// TODO: SECURITY WARNING - Signature validation not implemented
//
// This proposal has been validated for structural integrity and TxID correctness,
// but the signature in signedProp.Signature has NOT been cryptographically verified.
//
// To implement full signature validation for multi-org MSP:
// 1. Implement MSPManager to handle multiple organizations (not just single MSP)
// 2. Deserialize shdr.Creator to extract the signer's identity and public key
// 3. Verify signedProp.Signature over signedProp.ProposalBytes using the public key
// 4. Validate the signer's certificate chain against trusted CAs
// 5. Check access control policies (which identities can invoke which functions)
//
// Until then, this endorser should ONLY be deployed in trusted environments
// where proposal authenticity is guaranteed by other means (e.g., mtls,
// network-level access controls, or when used for testing purposes only).
func Parse(signedProp *peer.SignedProposal, expectedTime time.Time) (Invocation, error) {
	prop, err := protoutil.UnmarshalProposal(signedProp.ProposalBytes)
	if err != nil {
		return Invocation{}, err
	}
	if prop == nil {
		return Invocation{}, errors.New("proposal is empty")
	}

	hdr, err := protoutil.UnmarshalHeader(prop.Header)
	if err != nil {
		return Invocation{}, err
	}
	if hdr == nil {
		return Invocation{}, errors.New("header is empty")
	}

	chdr, err := protoutil.UnmarshalChannelHeader(hdr.ChannelHeader)
	if err != nil {
		return Invocation{}, err
	}

	// we only expect endorser transactions
	if common.HeaderType(chdr.Type) != common.HeaderType_ENDORSER_TRANSACTION {
		return Invocation{}, fmt.Errorf("invalid header type %s", common.HeaderType(chdr.Type))
	}

	// ensure the epoch is 0
	if chdr.Epoch != 0 {
		return Invocation{}, errors.New("epoch is non-zero")
	}

	// Validate timestamp to prevent replay attacks with old proposals
	if chdr.Timestamp == nil {
		return Invocation{}, fmt.Errorf("proposal timestamp is required")
	}
	if !expectedTime.IsZero() {
		timestamp := time.Unix(chdr.Timestamp.Seconds, int64(chdr.Timestamp.Nanos))
		// Allow 10 minute window (5 min past, 5 min future) to account for clock skew
		if timestamp.Before(timestamp.Add(-5*time.Minute)) || timestamp.After(timestamp.Add(5*time.Minute)) {
			return Invocation{}, fmt.Errorf("proposal timestamp %v outside valid window (now: %v)", timestamp, timestamp)
		}
	}

	shdr, err := protoutil.UnmarshalSignatureHeader(hdr.SignatureHeader)
	if err != nil {
		return Invocation{}, err
	}
	if len(shdr.Nonce) == 0 {
		return Invocation{}, errors.New("nonce is empty")
	}
	if len(shdr.Creator) == 0 {
		return Invocation{}, errors.New("creator is empty")
	}

	// ensure signature matches txid
	expected := protoutil.ComputeTxID(shdr.Nonce, shdr.Creator)
	if chdr.TxId != expected {
		return Invocation{}, fmt.Errorf("txid mismatch [%s != expected %s]", chdr.TxId, expected)
	}

	// proposal
	cpp, err := protoutil.UnmarshalChaincodeProposalPayload(prop.Payload)
	if err != nil {
		return Invocation{}, err
	}
	cis, err := protoutil.UnmarshalChaincodeInvocationSpec(cpp.Input)
	if err != nil {
		return Invocation{}, err
	}

	// check if there is a function
	if cis.ChaincodeSpec == nil || cis.ChaincodeSpec.Input == nil || len(cis.ChaincodeSpec.Input.Args) == 0 {
		return Invocation{}, fmt.Errorf("invalid spec %v for tx %s", cis, chdr.TxId)
	}

	// proposal hash is necessary for the endorsement in fabric 2 and 3
	propHash, err := protoutil.GetProposalHash1(hdr, prop.Payload)
	if err != nil {
		return Invocation{}, err
	}

	return Invocation{
		TxID:         chdr.TxId,
		Nonce:        shdr.Nonce,
		Creator:      shdr.Creator,
		Proposal:     prop,
		ProposalHash: propHash,
		Args:         cis.ChaincodeSpec.Input.Args,
		CCID:         cis.ChaincodeSpec.ChaincodeId,
		Channel:      chdr.ChannelId,
	}, nil
}
