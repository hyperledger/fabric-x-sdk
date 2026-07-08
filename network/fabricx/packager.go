/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"context"
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

// NewTxPackager returns a TxPackager that assembles Fabric-X transaction envelopes.
func NewTxPackager() TxPackager {
	return TxPackager{}
}

// TxPackager assembles a Fabric-X transaction envelope from an endorsement.
type TxPackager struct{}

// PackageTx combines the proposal and endorser responses into a Fabric-X envelope.
func (p TxPackager) PackageTx(end sdk.Endorsement) (*common.Envelope, error) {
	return CreateTx(end.Proposal, end.Responses...)
}

// CreateTx is an adaptation of protoutil.CreateSignedTx,
// tweaked to work with Fabric-X payloads. Fabric-X does not require the
// submitting client to sign the envelope, so no signer is involved.
func CreateTx(proposal *peer.Proposal, resps ...*peer.ProposalResponse) (*common.Envelope, error) {
	if len(resps) == 0 {
		return nil, errors.New("at least one proposal response is required")
	}

	// the original header
	hdr, err := protoutil.UnmarshalHeader(proposal.Header)
	if err != nil {
		return nil, err
	}

	shdr, err := protoutil.UnmarshalSignatureHeader(hdr.SignatureHeader)
	if err != nil {
		return nil, err
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

	// replace Fabric ENDORSER_PROPOSAL with Fabric-X MESSAGE
	chdr, err := protoutil.UnmarshalChannelHeader(hdr.ChannelHeader)
	if err != nil {
		return nil, err
	}
	chdr.Type = int32(common.HeaderType_MESSAGE)
	chdrBytes, err := proto.Marshal(chdr)
	if err != nil {
		return nil, err
	}

	shdr.Creator = nil

	// create the payload
	payl := &common.Payload{
		Header: &common.Header{
			ChannelHeader:   chdrBytes,
			SignatureHeader: protoutil.MarshalOrPanic(shdr),
		},
		Data: txBytes,
	}
	paylBytes, err := protoutil.GetBytesPayload(payl)
	if err != nil {
		return nil, err
	}

	// here's the envelope
	return &common.Envelope{Payload: paylBytes}, nil
}

// NewSubmitter is a convenience constructor that wires together a Fabric-X TxPackager
// and a FabricSubmitter for Fabric-X orderers.
func NewSubmitter(ctx context.Context, orderers []network.OrdererConf, waitAfterSubmit time.Duration, logger sdk.Logger) (*network.FabricSubmitter, error) {
	return network.NewSubmitter(ctx, orderers, NewTxPackager(), waitAfterSubmit, logger)
}
