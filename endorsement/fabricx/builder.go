/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"google.golang.org/protobuf/proto"
)

const (
	EventKey = "_event_"
	InputKey = "_input_"
)

func NewEndorsementBuilder(signer sdk.Signer) EndorsementBuilder {
	return EndorsementBuilder{signer: signer}
}

type EndorsementBuilder struct {
	signer sdk.Signer
}

// Endorse generates a signed proposal response based on the invocation and execution result.
// It follows the Fabric-X transaction and signature format, wrapped in a Fabric envelope.
func (e EndorsementBuilder) Endorse(inv endorsement.Invocation, res endorsement.ExecutionResult) (*peer.ProposalResponse, error) {
	var event []byte
	var err error

	// Fabric-X does not have a canonical way to register a chaincode event.
	// To help with compatibility, we provide the option to mimic the Fabric
	// behavior by adding a blind write under a known key.
	if len(res.Event) > 0 {
		event, err = proto.Marshal(&peer.ChaincodeEvent{
			Payload:     res.Event,
			ChaincodeId: inv.CCID.Name,
			TxId:        inv.TxID,
			EventName:   "log",
		})
		if err != nil {
			return nil, fmt.Errorf("marshal events: %w", err)
		}
		res.RWS.Writes = append(res.RWS.Writes, blocks.KVWrite{
			Key:   EventKey,
			Value: event,
		})
	}

	// Fabric-X does not store the chaincode invocation. We store the arguments
	// as a blind write with a known key to give the same options to users as
	// they have in Fabric. This lets the SDK behave the same across protocols.
	if len(inv.Args) > 0 {
		in, err := proto.Marshal(&peer.ChaincodeInput{Args: inv.Args})
		if err != nil {
			return nil, fmt.Errorf("marshal input: %w", err)
		}
		res.RWS.Writes = append(res.RWS.Writes, blocks.KVWrite{
			Key:   InputKey,
			Value: in,
		})
	}

	prpBytes, err := marshalRWSet(res.RWS, inv.CCID.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal read/write set: %w", err)
	}

	var tx applicationpb.Tx
	if err := proto.Unmarshal(prpBytes, &tx); err != nil {
		return nil, fmt.Errorf("failed to deserialize tx")
	}
	if len(tx.Namespaces) == 0 {
		return nil, errors.New("nothing to endorse")
	}

	digest, err := tx.Namespaces[0].ASN1Marshal(inv.TxID)
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

func marshalRWSet(rws blocks.ReadWriteSet, namespace string) ([]byte, error) {
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
	sort.Slice(readsOnly, func(i, j int) bool {
		return bytes.Compare(readsOnly[i].Key, readsOnly[j].Key) < 0
	})
	sort.Slice(readWrites, func(i, j int) bool {
		return bytes.Compare(readWrites[i].Key, readWrites[j].Key) < 0
	})
	sort.Slice(blindWrites, func(i, j int) bool {
		return bytes.Compare(blindWrites[i].Key, blindWrites[j].Key) < 0
	})

	tx := &applicationpb.Tx{
		Namespaces: []*applicationpb.TxNamespace{{
			NsId:        namespace,
			NsVersion:   0,
			ReadsOnly:   readsOnly,
			ReadWrites:  readWrites,
			BlindWrites: blindWrites,
		}},
	}

	rw, err := proto.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rwset: %w", err)
	}

	return rw, nil
}
