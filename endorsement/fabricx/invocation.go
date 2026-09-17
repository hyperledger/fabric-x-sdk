/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"crypto/rand"
	"errors"
	"fmt"

	commonpb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ endorsement.InvocationBuilder = InvocationBuilder{}

// nonceSize matches the Fabric builder, so transaction ids are computed
// over the same input width on both paths.
const nonceSize = 24

// NewInvocationBuilder returns an InvocationBuilder that produces header-only
// Fabric-X invocations.
func NewInvocationBuilder(signer sdk.Signer) InvocationBuilder {
	return InvocationBuilder{signer: signer}
}

// InvocationBuilder creates Fabric-X-format invocations from a signer.
type InvocationBuilder struct {
	signer sdk.Signer
}

// NewInvocation builds an invocation for the Fabric-X path. It carries only the
// header, leaving out the proposal payload, the proposal hash and the chaincode
// header extension, none of which a Fabric-X envelope reads.
//
// chaincodeVersion is Fabric's chaincode-version convention; it is unread on
// this path but accepted so both protocols' builders satisfy the same
// endorsement.InvocationBuilder interface. nsVersion is the namespace's
// current MVCC version counter — the committer rejects the resulting
// transaction as stale if it doesn't match the namespace's actual current
// version.
//
// Every call mints a fresh nonce and transaction id. Under a multi-endorser
// policy, build the invocation once and hand the same one to every endorser:
// the transaction id is part of what each endorser signs, so endorsers that
// each built their own would sign different digests. Their payloads can still
// match, which means the packager cannot always catch it.
//
// The result is not a proposal an endorser can parse. fabric.Parse reads
// the proposal payload, deliberately absent here, so this serves the local
// submit path rather than a request to a remote endorser.
func (b InvocationBuilder) NewInvocation(channel, namespace, chaincodeVersion string, nsVersion uint64, args [][]byte) (endorsement.Invocation, error) {
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
		TxID:             txID,
		Nonce:            nonce,
		Creator:          creator,
		Args:             args,
		Namespace:        namespace,
		ChaincodeVersion: chaincodeVersion,
		NsVersion:        nsVersion,
		Channel:          channel,
		Proposal:         &peer.Proposal{Header: hdr},
	}, nil
}
