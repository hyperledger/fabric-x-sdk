/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"fmt"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-sdk/api/metadatapb"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"google.golang.org/protobuf/proto"
)

// Metadata is the SDK-defined content of a Fabric-X transaction's metadata.
type Metadata struct {
	Event     []byte
	EventName string
	Payload   []byte
	InputArgs [][]byte
}

// EncodeMetadata marshals m into the transaction metadata. The SDK owns
// metadata[0], which holds a metadatapb.Metadata; later entries are free for
// other consumers. Marshaling is deterministic because every endorser of a
// transaction has to produce the same bytes.
func EncodeMetadata(m Metadata) ([][]byte, error) {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(&metadatapb.Metadata{
		InputArgs: m.InputArgs,
		Event:     m.Event,
		EventName: m.EventName,
		Payload:   m.Payload,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}
	return [][]byte{b}, nil
}

// DecodeMetadata extracts the event, event name, payload, and input args from
// metadata[0] of the transaction metadata (see EncodeMetadata). Missing or
// undecodable metadata yields an empty Metadata, since transactions not
// created by the SDK need not carry it.
func DecodeMetadata(metadata [][]byte) Metadata {
	if len(metadata) == 0 {
		return Metadata{}
	}
	var pm metadatapb.Metadata
	if err := proto.Unmarshal(metadata[0], &pm); err != nil {
		return Metadata{}
	}
	return Metadata{
		Event:     pm.Event,
		EventName: pm.EventName,
		Payload:   pm.Payload,
		InputArgs: pm.InputArgs,
	}
}

// DecodeNamespaces converts Fabric-X TxNamespace protos into the SDK's
// protocol-neutral per-namespace read/write sets.
//
// Writes never get IsDelete set, on purpose. Fabric-X has no delete: a nil
// value is stored as a NULL value at the next version, and the key keeps
// existing.
func DecodeNamespaces(namespaces []*applicationpb.TxNamespace) []blocks.NsReadWriteSet {
	nsRWS := make([]blocks.NsReadWriteSet, len(namespaces))
	for i, ns := range namespaces {
		nsrws := blocks.NsReadWriteSet{
			Namespace: ns.NsId,
			RWS: blocks.ReadWriteSet{
				Reads:  []blocks.KVRead{},
				Writes: []blocks.KVWrite{},
			},
		}

		for _, r := range ns.ReadsOnly {
			read := blocks.KVRead{Key: string(r.Key)}
			// Version nil means "no constraint" (new key / blind-write semantics).
			// Version 0 is a valid MVCC constraint: the key was first written at block 0.
			if r.Version != nil {
				read.Version = &blocks.Version{
					BlockNum: *r.Version,
				}
			}
			nsrws.RWS.Reads = append(nsrws.RWS.Reads, read)
		}
		for _, bw := range ns.BlindWrites {
			// All blind writes are now normal world state writes
			// (events and inputs are in metadata)
			nsrws.RWS.Writes = append(nsrws.RWS.Writes, blocks.KVWrite{
				Key:   string(bw.Key),
				Value: bw.Value,
			})
		}
		for _, rw := range ns.ReadWrites {
			read := blocks.KVRead{Key: string(rw.Key)}
			// Version nil means "no constraint" (new key / blind-write semantics).
			// Version 0 is a valid MVCC constraint: the key was first written at block 0.
			if rw.Version != nil {
				read.Version = &blocks.Version{
					BlockNum: *rw.Version,
				}
			}
			nsrws.RWS.Reads = append(nsrws.RWS.Reads, read)
			nsrws.RWS.Writes = append(nsrws.RWS.Writes, blocks.KVWrite{
				Key:   string(rw.Key),
				Value: rw.Value,
			})
		}

		nsRWS[i] = nsrws
	}
	return nsRWS
}
