/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"crypto/rand"
	"fmt"

	commonpb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// nonceSize matches endorsement.NewInvocation, so transaction ids are computed
// over the same input width on both paths.
const nonceSize = 24

// NewInvocation builds an invocation for the Fabric-X path. It carries only the
// header, leaving out the proposal payload, the proposal hash and the chaincode
// header extension, none of which a Fabric-X envelope reads. Use
// endorsement.NewInvocation for Fabric, where the peer needs the full proposal
// and the hash is part of the endorsement.
//
// Every call mints a fresh nonce and transaction id. Under a multi-endorser
// policy, build the invocation once and hand the same one to every endorser:
// the transaction id is part of what each endorser signs, so endorsers that
// each built their own would sign different digests. Their payloads can still
// match, which means the packager cannot always catch it.
//
// The result is not a proposal an endorser can parse. endorsement.Parse reads
// the proposal payload, deliberately absent here, so this serves the local
// submit path rather than a request to a remote endorser.
func NewInvocation(signer sdk.Signer, channel, namespace, nsVersion string, args [][]byte) (endorsement.Invocation, error) {
	creator, err := signer.Serialize()
	if err != nil {
		return endorsement.Invocation{}, err
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return endorsement.Invocation{}, fmt.Errorf("read nonce: %w", err)
	}

	txID := protoutil.ComputeTxID(nonce, creator)

	chdr, err := protoutil.Marshal(&commonpb.ChannelHeader{
		Type:      int32(commonpb.HeaderType_ENDORSER_TRANSACTION),
		TxId:      txID,
		Timestamp: timestamppb.Now(),
		ChannelId: channel,
	})
	if err != nil {
		return endorsement.Invocation{}, fmt.Errorf("marshal channel header: %w", err)
	}
	shdr, err := protoutil.Marshal(&commonpb.SignatureHeader{Nonce: nonce, Creator: creator})
	if err != nil {
		return endorsement.Invocation{}, fmt.Errorf("marshal signature header: %w", err)
	}
	hdr, err := protoutil.Marshal(&commonpb.Header{ChannelHeader: chdr, SignatureHeader: shdr})
	if err != nil {
		return endorsement.Invocation{}, fmt.Errorf("marshal header: %w", err)
	}

	return endorsement.Invocation{
		TxID:     txID,
		Nonce:    nonce,
		Creator:  creator,
		Args:     args,
		CCID:     &peer.ChaincodeID{Name: namespace, Version: nsVersion},
		Channel:  channel,
		Proposal: &peer.Proposal{Header: hdr},
	}, nil
}
