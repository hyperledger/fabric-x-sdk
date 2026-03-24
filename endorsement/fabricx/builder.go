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
	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	"github.com/hyperledger/fabric-x-committer/utils/signature"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"google.golang.org/protobuf/proto"
)

func NewEndorsementBuilder(signer sdk.Signer) EndorsementBuilder {
	return EndorsementBuilder{signer: signer}
}

type EndorsementBuilder struct {
	signer sdk.Signer
}

func (e EndorsementBuilder) Endorse(inv endorsement.Invocation, res endorsement.ExecutionResult) (*peer.ProposalResponse, error) {
	// TODO: event

	prpBytes, err := marshalRWSet(res.RWS, inv.CCID.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal read/write set: %w", err)
	}

	var tx protoblocktx.Tx
	if err := proto.Unmarshal(prpBytes, &tx); err != nil {
		return nil, fmt.Errorf("failed to deserialize tx")
	}
	if len(tx.Namespaces) == 0 {
		return nil, errors.New("nothing to endorse")
	}

	digest, err := signature.ASN1MarshalTxNamespace(inv.TxID, tx.Namespaces[0])
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

	readsOnly := make([]*protoblocktx.Read, 0)
	readWrites := make([]*protoblocktx.ReadWrite, 0)
	blindWrites := make([]*protoblocktx.Write, 0)

	for _, w := range writes {
		// TODO is this correct?
		// If so, do we need the IsDelete?
		if w.IsDelete {
			w.Value = nil
		}
		r, isRead := readByKey[w.Key]
		if isRead {
			delete(readByKey, w.Key)

			rw := &protoblocktx.ReadWrite{
				Key:   []byte(w.Key),
				Value: w.Value,
			}
			if r.Version != nil {
				rw.Version = &r.Version.BlockNum
			}
			readWrites = append(readWrites, rw)
		} else {
			blindWrites = append(blindWrites, &protoblocktx.Write{
				Key:   []byte(w.Key),
				Value: w.Value,
			})
		}
	}

	// add the remaining reads which are not read+writes.
	for _, r := range readByKey {
		read := &protoblocktx.Read{Key: []byte(r.Key)}
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

	tx := &protoblocktx.Tx{
		Namespaces: []*protoblocktx.TxNamespace{{
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
