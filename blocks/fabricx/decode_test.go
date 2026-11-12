/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"testing"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-sdk/api/metadatapb"
	"google.golang.org/protobuf/proto"
)

// TestDecodeNamespacesNilValueIsNotDelete pins that a nil-value write decodes as
// an ordinary write, not a delete. Fabric-X keeps the key and bumps its version,
// so stores must keep it as a versioned record (see DecodeNamespaces).
func TestDecodeNamespacesNilValueIsNotDelete(t *testing.T) {
	version := uint64(4)
	got := DecodeNamespaces([]*applicationpb.TxNamespace{{
		NsId:        "basic",
		BlindWrites: []*applicationpb.Write{{Key: []byte("blind")}},
		ReadWrites:  []*applicationpb.ReadWrite{{Key: []byte("rw"), Version: &version}},
	}})

	if len(got) != 1 || len(got[0].RWS.Writes) != 2 {
		t.Fatalf("expected 1 namespace with 2 writes, got %+v", got)
	}
	for _, w := range got[0].RWS.Writes {
		if w.IsDelete || w.Value != nil {
			t.Errorf("write %q = %+v, want a nil-value write without IsDelete", w.Key, w)
		}
	}
}

func TestMetadataRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		md   Metadata
	}{
		{name: "empty"},
		{
			name: "event, name and payload only",
			md:   Metadata{Event: []byte("evt"), EventName: "Transfer", Payload: []byte("payload")},
		},
		{
			name: "args only",
			md:   Metadata{InputArgs: [][]byte{[]byte("a"), []byte("b"), []byte("c")}},
		},
		{
			name: "all present, including an empty arg",
			md: Metadata{
				Event:     []byte("evt"),
				EventName: "log",
				Payload:   []byte("payload"),
				InputArgs: [][]byte{[]byte("x"), {}, []byte("y")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := EncodeMetadata(tt.md)
			if err != nil {
				t.Fatalf("EncodeMetadata: %v", err)
			}
			if len(enc) != 1 {
				t.Fatalf("expected 1 metadata entry, got %d", len(enc))
			}
			assertMetadata(t, DecodeMetadata(enc), tt.md)
		})
	}
}

func TestDecodeMetadata(t *testing.T) {
	sdkMD, err := proto.Marshal(&metadatapb.Metadata{Event: []byte("evt"), EventName: "Transfer"})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		metadata [][]byte
		want     Metadata
	}{
		{name: "nil metadata", metadata: nil},
		{name: "empty first entry", metadata: [][]byte{nil}},
		{name: "undecodable first entry", metadata: [][]byte{{0xff, 0xff}}},
		{
			name:     "later entries are ignored",
			metadata: [][]byte{sdkMD, []byte("other consumer")},
			want:     Metadata{Event: []byte("evt"), EventName: "Transfer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertMetadata(t, DecodeMetadata(tt.metadata), tt.want)
		})
	}
}

func assertMetadata(t *testing.T, got, want Metadata) {
	t.Helper()
	if string(got.Event) != string(want.Event) {
		t.Errorf("event: got %q, want %q", got.Event, want.Event)
	}
	if got.EventName != want.EventName {
		t.Errorf("event name: got %q, want %q", got.EventName, want.EventName)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Errorf("payload: got %q, want %q", got.Payload, want.Payload)
	}
	if len(got.InputArgs) != len(want.InputArgs) {
		t.Fatalf("args len: got %d, want %d", len(got.InputArgs), len(want.InputArgs))
	}
	for i := range want.InputArgs {
		if string(got.InputArgs[i]) != string(want.InputArgs[i]) {
			t.Errorf("args[%d]: got %q, want %q", i, got.InputArgs[i], want.InputArgs[i])
		}
	}
}
