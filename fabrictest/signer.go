/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"google.golang.org/protobuf/proto"
)

// MockSigner is a no-MSP sdk.Signer for use with fabrictest: fabrictest doesn't
// validate real certificates or signatures, so Sign returns a fixed placeholder.
// Serialize still must return a real proto.Marshal'd msp.SerializedIdentity —
// consumers of the endorsement (e.g. the packager) unmarshal it as protobuf.
type MockSigner struct{}

func (MockSigner) Sign([]byte) ([]byte, error) { return []byte("signature"), nil }

func (MockSigner) Serialize() ([]byte, error) {
	return proto.Marshal(&msp.SerializedIdentity{Mspid: "test-msp", IdBytes: []byte("serialised identity")})
}
