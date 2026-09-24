/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"testing"
)

func TestDecodeMetadata(t *testing.T) {
	tests := []struct {
		name          string
		metadata      [][]byte
		wantEvent     []byte
		wantEventName string
		wantPayload   []byte
		wantArgs      [][]byte
	}{
		{
			name:     "nil metadata",
			metadata: nil,
		},
		{
			name:     "all absent, zero args",
			metadata: [][]byte{nil, nil, nil, {0}},
		},
		{
			name:          "event, name and payload only, zero args",
			metadata:      [][]byte{[]byte("evt"), []byte("Transfer"), []byte("payload"), {0}},
			wantEvent:     []byte("evt"),
			wantEventName: "Transfer",
			wantPayload:   []byte("payload"),
		},
		{
			name:     "one arg",
			metadata: [][]byte{nil, nil, nil, {1}, []byte("a")},
			wantArgs: [][]byte{[]byte("a")},
		},
		{
			name:     "many args",
			metadata: [][]byte{nil, nil, nil, {3}, []byte("a"), []byte("b"), []byte("c")},
			wantArgs: [][]byte{[]byte("a"), []byte("b"), []byte("c")},
		},
		{
			name:          "mixed: event, name, payload, and args all present",
			metadata:      [][]byte{[]byte("evt"), []byte("log"), []byte("payload"), {2}, []byte("x"), []byte("y")},
			wantEvent:     []byte("evt"),
			wantEventName: "log",
			wantPayload:   []byte("payload"),
			wantArgs:      [][]byte{[]byte("x"), []byte("y")},
		},
		{
			name:     "count present but args truncated",
			metadata: [][]byte{nil, nil, nil, {2}, []byte("only-one")},
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := DecodeMetadata(tt.metadata)
			if string(md.Event) != string(tt.wantEvent) {
				t.Errorf("event: got %q, want %q", md.Event, tt.wantEvent)
			}
			if md.EventName != tt.wantEventName {
				t.Errorf("event name: got %q, want %q", md.EventName, tt.wantEventName)
			}
			if string(md.Payload) != string(tt.wantPayload) {
				t.Errorf("payload: got %q, want %q", md.Payload, tt.wantPayload)
			}
			if len(md.InputArgs) != len(tt.wantArgs) {
				t.Fatalf("args len: got %d, want %d", len(md.InputArgs), len(tt.wantArgs))
			}
			for i, want := range tt.wantArgs {
				if string(md.InputArgs[i]) != string(want) {
					t.Errorf("args[%d]: got %q, want %q", i, md.InputArgs[i], want)
				}
			}
		})
	}
}
