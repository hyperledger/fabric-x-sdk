/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// Metadata is the SDK-defined content of a Fabric-X transaction's metadata.
type Metadata struct {
	Event     []byte
	EventName string
	Payload   []byte
	InputArgs [][]byte
}

// DecodeMetadata extracts the event, event name, payload, and input args from the transaction metadata.
// The layout is purely positional: metadata[0] = event, metadata[1] = event name,
// metadata[2] = payload, metadata[3] = arg count (1 byte), metadata[4:4+count] = args.
func DecodeMetadata(metadata [][]byte) Metadata {
	var m Metadata
	if len(metadata) > 0 {
		m.Event = metadata[0]
	}
	if len(metadata) > 1 {
		m.EventName = string(metadata[1])
	}
	if len(metadata) > 2 {
		m.Payload = metadata[2]
	}
	if len(metadata) > 3 && len(metadata[3]) > 0 {
		count := int(metadata[3][0])
		if end := 4 + count; end <= len(metadata) {
			m.InputArgs = metadata[4:end]
		}
	}
	return m
}

// DecodeNamespaces converts Fabric-X TxNamespace protos into the SDK's
// protocol-neutral per-namespace read/write sets.
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
