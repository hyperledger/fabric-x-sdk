/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"fmt"
	"sort"

	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/ledger/rwset/kvrwset"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric/protoutil"
	"google.golang.org/protobuf/proto"
)

func NewEndorsementBuilder(signer sdk.Signer) EndorsementBuilder {
	return EndorsementBuilder{signer: signer}
}

type EndorsementBuilder struct {
	signer sdk.Signer
}

// Endorse takes the relevant parts of an Invocation, a read/write set and optional event,
func (e EndorsementBuilder) Endorse(in endorsement.Invocation, res endorsement.ExecutionResult) (*peer.ProposalResponse, error) {
	simResBytes, err := marshalRWSet(&res.RWS, in.CCID.Name)
	if err != nil {
		return nil, fmt.Errorf("marshal rwset: %w", err)
	}

	var event []byte
	if len(res.Event) > 0 {
		event, err = proto.Marshal(&peer.ChaincodeEvent{
			Payload:     res.Event,
			ChaincodeId: in.CCID.Name,
			TxId:        in.TxID,
			EventName:   "log",
		})
		if err != nil {
			return nil, fmt.Errorf("marshal events: %w", err)
		}
	}

	prpBytes, err := protoutil.GetBytesProposalResponsePayload(in.ProposalHash, &peer.Response{}, simResBytes, event, in.CCID)
	if err != nil {
		return nil, fmt.Errorf("marshal response: %w", err)
	}

	// serialize the signing identity
	identityBytes, err := e.signer.Serialize()
	if err != nil {
		return nil, err
	}

	// sign the concatenation of the proposal response and the serialized endorser identity with this endorser's key
	sig, err := e.signer.Sign(append(prpBytes, identityBytes...))
	if err != nil {
		return nil, err
	}

	return &peer.ProposalResponse{
		Version: 1,
		Endorsement: &peer.Endorsement{
			Signature: sig,
			Endorser:  identityBytes,
		},
		Payload: prpBytes,
		Response: &peer.Response{
			Status:  res.Status,
			Message: res.Message,
			Payload: res.Payload,
		},
	}, nil
}

func marshalRWSet(res *blocks.ReadWriteSet, namespace string) ([]byte, error) {
	rws := &kvrwset.KVRWSet{
		Reads:  make([]*kvrwset.KVRead, len(res.Reads)),
		Writes: make([]*kvrwset.KVWrite, len(res.Writes)),
	}

	sort.Slice(res.Reads, func(i, j int) bool {
		return res.Reads[i].Key < res.Reads[j].Key
	})

	for i, r := range res.Reads {
		read := &kvrwset.KVRead{Key: r.Key}
		if r.Version != nil {
			read.Version = &kvrwset.Version{
				BlockNum: r.Version.BlockNum,
				TxNum:    uint64(r.Version.TxNum),
			}
		}
		rws.Reads[i] = read
	}

	sort.Slice(res.Writes, func(i, j int) bool {
		return res.Writes[i].Key < res.Writes[j].Key
	})

	for i, w := range res.Writes {
		rws.Writes[i] = &kvrwset.KVWrite{
			Key:      w.Key,
			IsDelete: w.IsDelete,
			Value:    w.Value,
		}
	}

	return protoutil.MarshalOrPanic(&rwset.TxReadWriteSet{
		NsRwset: []*rwset.NsReadWriteSet{
			{
				Namespace: namespace,
				Rwset:     protoutil.MarshalOrPanic(rws),
			},
		},
	}), nil
}
