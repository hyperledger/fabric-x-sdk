/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/hyperledger/fabric-x-common/protoutil"
)

var errBrokenSigner = errors.New("serialize failed")

// ecdsaSigner is a minimal, real sdk.Signer: Serialize returns the raw public key point (there is
// no MSP identity involved in a Deliver seek request), and Sign is a plain ECDSA signature over the
// SHA-256 digest, verifiable with ecdsa.VerifyASN1.
type ecdsaSigner struct {
	key *ecdsa.PrivateKey
}

func newECDSASigner(t *testing.T) ecdsaSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return ecdsaSigner{key: key}
}

func (s ecdsaSigner) Serialize() ([]byte, error) { return s.key.PublicKey.Bytes() }

func (s ecdsaSigner) Sign(msg []byte) ([]byte, error) {
	digest := sha256.Sum256(msg)
	return ecdsa.SignASN1(rand.Reader, s.key, digest[:])
}

func TestNewDeliverSeekInfo_Unsigned(t *testing.T) {
	env, err := newDeliverSeekInfo(nil, "mychannel", 3)
	if err != nil {
		t.Fatalf("newDeliverSeekInfo: %v", err)
	}
	if len(env.Signature) != 0 {
		t.Errorf("expected no signature, got %d bytes", len(env.Signature))
	}

	payl, err := protoutil.UnmarshalPayload(env.Payload)
	if err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	shdr, err := protoutil.UnmarshalSignatureHeader(payl.Header.SignatureHeader)
	if err != nil {
		t.Fatalf("unmarshal signature header: %v", err)
	}
	if len(shdr.Creator) != 0 {
		t.Errorf("expected no creator, got %d bytes", len(shdr.Creator))
	}
	if len(shdr.Nonce) == 0 {
		t.Error("the nonce must be kept even when unsigned")
	}

	chdr, err := protoutil.UnmarshalChannelHeader(payl.Header.ChannelHeader)
	if err != nil {
		t.Fatalf("unmarshal channel header: %v", err)
	}
	if chdr.ChannelId != "mychannel" {
		t.Errorf("unexpected channel id: %q", chdr.ChannelId)
	}
}

func TestNewDeliverSeekInfo_Signed(t *testing.T) {
	signer := newECDSASigner(t)
	wantCreator, err := signer.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}

	env, err := newDeliverSeekInfo(signer, "mychannel", 3)
	if err != nil {
		t.Fatalf("newDeliverSeekInfo: %v", err)
	}

	payl, err := protoutil.UnmarshalPayload(env.Payload)
	if err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	shdr, err := protoutil.UnmarshalSignatureHeader(payl.Header.SignatureHeader)
	if err != nil {
		t.Fatalf("unmarshal signature header: %v", err)
	}
	if string(shdr.Creator) != string(wantCreator) {
		t.Errorf("creator does not match the signer's identity: got %x, want %x", shdr.Creator, wantCreator)
	}

	digest := sha256.Sum256(env.Payload)
	if !ecdsa.VerifyASN1(&signer.key.PublicKey, digest[:], env.Signature) {
		t.Error("signature does not verify over the payload")
	}
}

func TestNewDeliverSeekInfo_SignerError(t *testing.T) {
	if _, err := newDeliverSeekInfo(brokenPeerSigner{}, "mychannel", 0); err == nil {
		t.Error("expected the serialize error to surface")
	}
}

type brokenPeerSigner struct{}

func (brokenPeerSigner) Serialize() ([]byte, error)  { return nil, errBrokenSigner }
func (brokenPeerSigner) Sign([]byte) ([]byte, error) { return nil, errBrokenSigner }
