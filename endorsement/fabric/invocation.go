/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
)

var _ endorsement.InvocationBuilder = InvocationBuilder{}

// nonceSize matches the Fabric-X builder, so transaction ids are computed
// over the same input width on both paths.
const nonceSize = 24

// NewInvocationBuilder returns an InvocationBuilder that produces a full
// chaincode proposal. The peer needs the proposal, and the hash is part of
// the endorsement.
func NewInvocationBuilder(signer sdk.Signer) InvocationBuilder {
	return InvocationBuilder{signer: signer}
}

// InvocationBuilder creates Fabric-format invocations from a signer.
type InvocationBuilder struct {
	signer sdk.Signer
}

// NewInvocation creates an Invocation from channel, namespace, chaincode
// version and args. chaincodeVersion must match the namespace's approved
// chaincode version, or the peer rejects the resulting proposal as
// INVALID_CHAINCODE. nsVersion is a Fabric-X concept (the MVCC namespace
// counter) and is ignored here; it's accepted so both protocols' builders
// satisfy the same endorsement.InvocationBuilder interface.
func (b InvocationBuilder) NewInvocation(channel, namespace, chaincodeVersion string, _ uint64, args [][]byte) (endorsement.Invocation, error) {
	if b.signer == nil {
		return endorsement.Invocation{}, errors.New("nil signer")
	}

	creator, err := b.signer.Serialize()
	if err != nil {
		return endorsement.Invocation{}, err
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return endorsement.Invocation{}, fmt.Errorf("read nonce: %w", err)
	}

	txID := protoutil.ComputeTxID(nonce, creator)
	ccid := &peer.ChaincodeID{Name: namespace, Version: chaincodeVersion}
	proposal, _, err := protoutil.CreateChaincodeProposalWithTxIDNonceAndTransient(
		txID,
		common.HeaderType_ENDORSER_TRANSACTION,
		channel,
		&peer.ChaincodeInvocationSpec{
			ChaincodeSpec: &peer.ChaincodeSpec{
				Type:        peer.ChaincodeSpec_CAR,
				ChaincodeId: ccid,
				Input:       &peer.ChaincodeInput{Args: args},
			},
		},
		nonce,
		creator,
		nil,
	)
	if err != nil {
		return endorsement.Invocation{}, err
	}

	hdr, err := protoutil.UnmarshalHeader(proposal.Header)
	if err != nil {
		return endorsement.Invocation{}, err
	}
	propHash, err := protoutil.GetProposalHash1(hdr, proposal.Payload)
	if err != nil {
		return endorsement.Invocation{}, err
	}

	return endorsement.Invocation{
		TxID:             txID,
		Nonce:            nonce,
		Creator:          creator,
		Args:             args,
		Namespace:        ccid.Name,
		ChaincodeVersion: ccid.Version,
		Channel:          channel,
		Proposal:         proposal,
		ProposalHash:     propHash,
	}, nil
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
//
// Parse has no Fabric-X counterpart: Fabric-X has no remote-endorser-receives-a-
// SignedProposal flow to parse at all.
func Parse(signedProp *peer.SignedProposal, expectedTime time.Time) (endorsement.Invocation, error) {
	prop, err := protoutil.UnmarshalProposal(signedProp.ProposalBytes)
	if err != nil {
		return endorsement.Invocation{}, err
	}
	if prop == nil {
		return endorsement.Invocation{}, errors.New("proposal is empty")
	}

	hdr, err := protoutil.UnmarshalHeader(prop.Header)
	if err != nil {
		return endorsement.Invocation{}, err
	}
	if hdr == nil {
		return endorsement.Invocation{}, errors.New("header is empty")
	}

	chdr, err := protoutil.UnmarshalChannelHeader(hdr.ChannelHeader)
	if err != nil {
		return endorsement.Invocation{}, err
	}

	// we only expect endorser transactions
	if common.HeaderType(chdr.Type) != common.HeaderType_ENDORSER_TRANSACTION {
		return endorsement.Invocation{}, fmt.Errorf("invalid header type %s", common.HeaderType(chdr.Type))
	}

	// ensure the epoch is 0
	if chdr.Epoch != 0 {
		return endorsement.Invocation{}, errors.New("epoch is non-zero")
	}

	// Validate timestamp to prevent replay attacks with old proposals
	if chdr.Timestamp == nil {
		return endorsement.Invocation{}, fmt.Errorf("proposal timestamp is required")
	}
	if !expectedTime.IsZero() {
		timestamp := time.Unix(chdr.Timestamp.Seconds, int64(chdr.Timestamp.Nanos))
		// Allow 10 minute window (5 min past, 5 min future) to account for clock skew
		if timestamp.Before(expectedTime.Add(-5*time.Minute)) || timestamp.After(expectedTime.Add(5*time.Minute)) {
			return endorsement.Invocation{}, fmt.Errorf("proposal timestamp %v outside valid window (expected: %v)", timestamp, expectedTime)
		}
	}

	shdr, err := protoutil.UnmarshalSignatureHeader(hdr.SignatureHeader)
	if err != nil {
		return endorsement.Invocation{}, err
	}
	if len(shdr.Nonce) == 0 {
		return endorsement.Invocation{}, errors.New("nonce is empty")
	}
	if len(shdr.Creator) == 0 {
		return endorsement.Invocation{}, errors.New("creator is empty")
	}

	// ensure signature matches txid
	expected := protoutil.ComputeTxID(shdr.Nonce, shdr.Creator)
	if chdr.TxId != expected {
		return endorsement.Invocation{}, fmt.Errorf("txid mismatch [%s != expected %s]", chdr.TxId, expected)
	}

	// proposal
	cpp, err := protoutil.UnmarshalChaincodeProposalPayload(prop.Payload)
	if err != nil {
		return endorsement.Invocation{}, err
	}
	cis, err := protoutil.UnmarshalChaincodeInvocationSpec(cpp.Input)
	if err != nil {
		return endorsement.Invocation{}, err
	}

	// check if there is a function
	if cis.ChaincodeSpec == nil || cis.ChaincodeSpec.Input == nil || len(cis.ChaincodeSpec.Input.Args) == 0 {
		return endorsement.Invocation{}, fmt.Errorf("invalid spec %v for tx %s", cis, chdr.TxId)
	}

	// proposal hash is necessary for the endorsement in fabric 2 and 3
	propHash, err := protoutil.GetProposalHash1(hdr, prop.Payload)
	if err != nil {
		return endorsement.Invocation{}, err
	}

	ccid := cis.ChaincodeSpec.ChaincodeId
	return endorsement.Invocation{
		TxID:             chdr.TxId,
		Nonce:            shdr.Nonce,
		Creator:          shdr.Creator,
		Proposal:         prop,
		ProposalHash:     propHash,
		Args:             cis.ChaincodeSpec.Input.Args,
		Namespace:        ccid.GetName(),
		ChaincodeVersion: ccid.GetVersion(),
		Channel:          chdr.ChannelId,
	}, nil
}
