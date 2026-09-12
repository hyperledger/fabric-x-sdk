/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"google.golang.org/protobuf/proto"
)

// NewEndorsementBuilder returns an EndorsementBuilder that produces Fabric-X-format signed responses.
func NewEndorsementBuilder(signer sdk.Signer) EndorsementBuilder {
	return EndorsementBuilder{signer: signer}
}

// EndorsementBuilder creates Fabric-X-format signed ProposalResponses wrapped in a Fabric envelope.
type EndorsementBuilder struct {
	signer sdk.Signer
}

// Endorse generates a signed proposal response based on the invocation and execution result.
// It follows the Fabric-X transaction and signature format, wrapped in a Fabric envelope.
func (e EndorsementBuilder) Endorse(inv endorsement.Invocation, res endorsement.ExecutionResult) (*peer.ProposalResponse, error) {
	// Metadata is always exactly two entries, [0] = input args, [1] = events,
	// so DecodeMetadata's fixed positions never drift when one of them is absent.
	var inputBytes []byte
	if len(inv.Args) > 0 {
		var err error
		inputBytes, err = proto.Marshal(&peer.ChaincodeInput{Args: inv.Args})
		if err != nil {
			return nil, fmt.Errorf("marshal input: %w", err)
		}
	}

	var eventBytes []byte
	if len(res.Event) > 0 {
		var err error
		eventBytes, err = proto.Marshal(&peer.ChaincodeEvent{
			Payload:     res.Event,
			ChaincodeId: inv.CCID.Name,
			TxId:        inv.TxID,
			EventName:   "log",
		})
		if err != nil {
			return nil, fmt.Errorf("marshal events: %w", err)
		}
	}

	metadata := [][]byte{inputBytes, eventBytes}

	tx := buildTx(res.RWS, inv.CCID.Name, metadata)
	prpBytes, err := proto.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal read/write set: %w", err)
	}

	digest, err := tx.Namespaces[0].ASN1Marshal(inv.TxID, metadata)
	if err != nil {
		return nil, fmt.Errorf("cannot serialize tx: %w", err)
	}

	identityBytes, err := e.signer.Serialize()
	if err != nil {
		return nil, err
	}
	sig, err := e.signer.Sign(digest)
	if err != nil {
		return nil, fmt.Errorf("could not sign the proposal response payload: %w", err)
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

// buildTx splits the read-write set into the three Fabric-X access kinds, sorted
// by key so the result does not depend on the caller's ordering. Every endorser
// of a transaction has to produce the same bytes, so this stays deterministic.
func buildTx(rws blocks.ReadWriteSet, namespace string, metadata [][]byte) *applicationpb.Tx {
	writes := append([]blocks.KVWrite(nil), rws.Writes...)
	readByKey := make(map[string]blocks.KVRead, len(rws.Reads))
	for _, r := range rws.Reads {
		readByKey[r.Key] = r
	}

	readsOnly := make([]*applicationpb.Read, 0)
	readWrites := make([]*applicationpb.ReadWrite, 0)
	blindWrites := make([]*applicationpb.Write, 0)

	for _, w := range writes {
		// TODO is this correct?
		// If so, do we need the IsDelete?
		if w.IsDelete {
			w.Value = nil
		}
		r, isRead := readByKey[w.Key]
		if isRead {
			delete(readByKey, w.Key)

			rw := &applicationpb.ReadWrite{
				Key:   []byte(w.Key),
				Value: w.Value,
			}
			if r.Version != nil {
				rw.Version = &r.Version.BlockNum
			}
			readWrites = append(readWrites, rw)
		} else {
			blindWrites = append(blindWrites, &applicationpb.Write{
				Key:   []byte(w.Key),
				Value: w.Value,
			})
		}
	}

	// add the remaining reads which are not read+writes.
	for _, r := range readByKey {
		read := &applicationpb.Read{Key: []byte(r.Key)}
		if r.Version != nil {
			read.Version = &r.Version.BlockNum
		}
		readsOnly = append(readsOnly, read)
	}

	// sort the results
	slices.SortFunc(readsOnly, func(a, b *applicationpb.Read) int {
		return bytes.Compare(a.Key, b.Key)
	})
	slices.SortFunc(readWrites, func(a, b *applicationpb.ReadWrite) int {
		return bytes.Compare(a.Key, b.Key)
	})
	slices.SortFunc(blindWrites, func(a, b *applicationpb.Write) int {
		return bytes.Compare(a.Key, b.Key)
	})

	return &applicationpb.Tx{
		Metadata: metadata,
		Namespaces: []*applicationpb.TxNamespace{{
			NsId:        namespace,
			NsVersion:   0,
			ReadsOnly:   readsOnly,
			ReadWrites:  readWrites,
			BlindWrites: blindWrites,
		}},
	}
}
