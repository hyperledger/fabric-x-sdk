/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	b64 "encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/network"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/protobuf/proto"
)

func NewTxPackager(s sdk.Signer) TxPackager {
	return TxPackager{
		signer: s,
	}
}

type TxPackager struct {
	signer sdk.Signer
}

func (p TxPackager) PackageTx(end sdk.Endorsement) (*common.Envelope, error) {
	return CreateSignedTx(end.Proposal, p.signer, end.Responses...)
}

// CreateSignedTx is an adaptation of protoutil.CreateSignedTx,
// tweaked to work with Fabric-X payloads and signature schemes.
func CreateSignedTx(
	proposal *peer.Proposal,
	signer sdk.Signer,
	resps ...*peer.ProposalResponse,
) (*common.Envelope, error) {
	if len(resps) == 0 {
		return nil, errors.New("at least one proposal response is required")
	}

	if signer == nil {
		return nil, errors.New("signer is required when creating a signed transaction")
	}

	// the original header
	hdr, err := protoutil.UnmarshalHeader(proposal.Header)
	if err != nil {
		return nil, err
	}

	// check that the signer is the same that is referenced in the header
	signerBytes, err := signer.Serialize()
	if err != nil {
		return nil, err
	}

	shdr, err := protoutil.UnmarshalSignatureHeader(hdr.SignatureHeader)
	if err != nil {
		return nil, err
	}

	if !bytes.Equal(signerBytes, shdr.Creator) {
		return nil, errors.New("signer must be the same as the one referenced in the header")
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
	var tx protoblocktx.Tx
	if err := proto.Unmarshal(a1, &tx); err != nil {
		return nil, fmt.Errorf("expected protoblocktx.Tx endorsement payload, %s", err.Error())
	}
	for _, end := range endorsements {
		tx.Signatures = append(tx.Signatures, end.Signature)
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

	// create the payload
	payl := &common.Payload{
		Header: &common.Header{
			ChannelHeader:   chdrBytes,
			SignatureHeader: hdr.SignatureHeader,
		},
		Data: txBytes,
	}
	paylBytes, err := protoutil.GetBytesPayload(payl)
	if err != nil {
		return nil, err
	}

	// sign the payload
	sig, err := signer.Sign(paylBytes)
	if err != nil {
		return nil, err
	}

	// here's the envelope
	return &common.Envelope{Payload: paylBytes, Signature: sig}, nil
}

func NewSubmitter(orderers []network.OrdererConf, s sdk.Signer, waitAfterSubmit time.Duration, logger sdk.Logger) (*network.FabricSubmitter, error) {
	return network.NewSubmitter(orderers, NewTxPackager(s), waitAfterSubmit, logger)
}
