/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"context"
	"crypto/rand"
	b64 "encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/msppb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/network"
	"google.golang.org/protobuf/proto"
)

// envelopeNonceSize matches the convention used when the invocation itself was built
// (endorsement/fabricx.nonceSize); the two nonces are otherwise unrelated.
const envelopeNonceSize = 24

// NewTxPackager returns a TxPackager that assembles Fabric-X transaction envelopes.
//
// Signer is only required if the orderer enforces the channel Writers policy on submitted requests
// (fabric-x-orderer: General.ClientSignatureVerificationRequired). The SDK cannot detect that
// setting: an unsigned envelope sent to such an orderer is rejected by the router only after Submit
// has already returned nil.
//
// With a nil signer the envelopes carry no creator and no signature, which saves a certificate and
// a signature per transaction.
func NewTxPackager(signer sdk.Signer) TxPackager {
	return TxPackager{signer: signer}
}

// TxPackager assembles a Fabric-X transaction envelope from an endorsement, signed by the
// submitting client if it has a signer. The zero value packages unsigned envelopes.
type TxPackager struct {
	signer sdk.Signer
}

// PackageTx combines the proposal and endorser responses into a Fabric-X envelope, signed if the
// packager has a signer (General.ClientSignatureVerificationRequired=true on the orderer).
func (p TxPackager) PackageTx(end sdk.Endorsement) (*common.Envelope, error) {
	return CreateTx(end.Proposal, p.signer, end.Responses...)
}

// CreateTx is an adaptation of protoutil.CreateSignedTx, tweaked to work with Fabric-X payloads.
// It assembles an envelope from proposal and resps, signed by signer if it is not nil.
func CreateTx(proposal *peer.Proposal, signer sdk.Signer, resps ...*peer.ProposalResponse) (*common.Envelope, error) {
	if len(resps) == 0 {
		return nil, errors.New("at least one proposal response is required")
	}

	// ensure that all actions are bitwise equal and that they are successful
	var a1 []byte
	for n, r := range resps {
		if r.Response.Status < 200 || r.Response.Status >= 400 {
			return nil, fmt.Errorf("proposal response was not successful, error code %d, msg %s", r.Response.Status, r.Response.Message)
		}

		if n == 0 {
			a1 = r.Payload
			continue
		}

		if !bytes.Equal(a1, r.Payload) {
			return nil, fmt.Errorf("ProposalResponsePayloads do not match (base64): '%s' vs '%s'",
				b64.StdEncoding.EncodeToString(r.Payload), b64.StdEncoding.EncodeToString(a1))
		}
	}

	// fill endorsements according to their uniqueness
	endorsersUsed := make(map[string]struct{})
	var endorsements []*peer.Endorsement
	for _, r := range resps {
		if r.Endorsement == nil {
			continue
		}
		key := string(r.Endorsement.Endorser)
		if _, used := endorsersUsed[key]; used {
			continue
		}
		endorsements = append(endorsements, r.Endorsement)
		endorsersUsed[key] = struct{}{}
	}

	if len(endorsements) == 0 {
		return nil, fmt.Errorf("no endorsements")
	}

	// add endorser signatures to tx payload
	var tx applicationpb.Tx
	if err := proto.Unmarshal(a1, &tx); err != nil {
		return nil, fmt.Errorf("expected applicationpb.Tx endorsement payload, %s", err.Error())
	}
	nsEndorsements := &applicationpb.Endorsements{}
	for _, end := range endorsements {
		// Deserialize the endorser identity (msppb.Identity from SerializeWithIDOfCert or Serialize)
		endorserIdentity := &msppb.Identity{}
		if err := proto.Unmarshal(end.Endorser, endorserIdentity); err != nil {
			return nil, fmt.Errorf("failed to unmarshal endorser identity: %w", err)
		}
		nsEndorsements.EndorsementsWithIdentity = append(nsEndorsements.EndorsementsWithIdentity,
			&applicationpb.EndorsementWithIdentity{
				Endorsement: end.Signature,
				Identity:    endorserIdentity,
			},
		)
	}
	tx.Endorsements = make([]*applicationpb.Endorsements, len(tx.Namespaces))
	for i := range tx.Namespaces {
		tx.Endorsements[i] = nsEndorsements
	}
	txBytes, err := proto.Marshal(&tx)
	if err != nil {
		return nil, errors.New("can't marshal transaction payload")
	}

	shdr := &common.SignatureHeader{}
	if signer != nil {
		creator, err := signer.Serialize()
		if err != nil {
			return nil, fmt.Errorf("serialize signer: %w", err)
		}
		nonce := make([]byte, envelopeNonceSize)
		if _, err := rand.Read(nonce); err != nil {
			return nil, fmt.Errorf("read nonce: %w", err)
		}
		shdr.Creator, shdr.Nonce = creator, nonce
	}
	shdrBytes, err := proto.Marshal(shdr)
	if err != nil {
		return nil, fmt.Errorf("marshal signature header: %w", err)
	}

	// the proposal's channel header (channel, tx id, timestamp) carries over unchanged.
	hdr, err := protoutil.UnmarshalHeader(proposal.Header)
	if err != nil {
		return nil, err
	}

	// create the payload
	payl := &common.Payload{
		Header: &common.Header{
			ChannelHeader:   hdr.ChannelHeader,
			SignatureHeader: shdrBytes,
		},
		Data: txBytes,
	}
	paylBytes, err := protoutil.GetBytesPayload(payl)
	if err != nil {
		return nil, err
	}

	if signer == nil {
		return &common.Envelope{Payload: paylBytes}, nil
	}

	// sign the payload bytes, as the orderer checks the signature over exactly these
	sig, err := signer.Sign(paylBytes)
	if err != nil {
		return nil, fmt.Errorf("sign envelope payload: %w", err)
	}
	return &common.Envelope{Payload: paylBytes, Signature: sig}, nil
}

// NewSubmitter is a convenience constructor that wires together a Fabric-X TxPackager
// and a Submitter for Fabric-X orderers.
//
// The signer may be nil, in which case envelopes are unsigned. Pass one whenever the orderer verifies
// client signatures (ClientSignatureVerificationRequired), which is a sensible production setting
// because the orderer then enforces who may submit transactions. See NewTxPackager.
func NewSubmitter(ctx context.Context, orderers []network.OrdererConf, signer sdk.Signer, waitAfterSubmit time.Duration, logger sdk.Logger) (*network.Submitter, error) {
	return network.NewSubmitter(ctx, orderers, NewTxPackager(signer), waitAfterSubmit, logger)
}
