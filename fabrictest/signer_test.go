/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/protobuf/proto"
)

var _ sdk.Signer = MockSigner{}

func TestMockSigner_SerializeRoundTrips(t *testing.T) {
	raw, err := MockSigner{}.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	var id msp.SerializedIdentity
	if err := proto.Unmarshal(raw, &id); err != nil {
		t.Fatalf("Serialize produced bytes that don't parse as msp.SerializedIdentity: %v", err)
	}
	if id.Mspid == "" || len(id.IdBytes) == 0 {
		t.Fatalf("expected a non-empty Mspid and IdBytes, got %+v", &id)
	}
}

func TestMockSigner_Sign(t *testing.T) {
	sig, err := MockSigner{}.Sign([]byte("anything"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("expected a non-empty signature")
	}
}
