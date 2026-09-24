/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	efabx "github.com/hyperledger/fabric-x-sdk/endorsement/fabricx"
	"github.com/hyperledger/fabric-x-sdk/identity"
)

// newMSPSigner writes a self-signed ECDSA identity to a temporary MSP folder in
// the layout identity.SignerFromMSP reads, and loads it. It returns the signer
// exactly as production code builds one, along with the certificate PEM.
func newMSPSigner(t *testing.T, mspID string) (identity.Signer, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "user@" + mspID, Organization: []string{mspID}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	dir := t.TempDir()
	for name, content := range map[string][]byte{
		filepath.Join("keystore", "priv_sk"):   pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		filepath.Join("signcerts", "cert.pem"): certPEM,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	s, err := identity.SignerFromMSP(dir, mspID)
	if err != nil {
		t.Fatalf("SignerFromMSP: %v", err)
	}
	return s, certPEM
}

// newEndorsement returns a proposal created by client and a response from a
// different org's endorser, which is what CreateTx packages.
func newEndorsement(t *testing.T, client sdk.Signer) sdk.Endorsement {
	t.Helper()
	endorser, _ := newMSPSigner(t, "Org2MSP")

	inv, err := efabx.NewInvocationBuilder(client).NewInvocation("mychannel", "myns", "v1", 3, [][]byte{[]byte("fn")})
	if err != nil {
		t.Fatalf("NewInvocation: %v", err)
	}
	res := endorsement.Success(blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: "a", Value: []byte("va")}},
	}, nil, nil)
	resp, err := efabx.NewEndorsementBuilder(endorser).Endorse(inv, res)
	if err != nil {
		t.Fatalf("Endorse: %v", err)
	}
	return sdk.Endorsement{Proposal: inv.Proposal, Responses: []*peer.ProposalResponse{resp}}
}

// unpack returns the payload and its signature header from an envelope.
func unpack(t *testing.T, env *common.Envelope) (*common.Payload, *common.SignatureHeader) {
	t.Helper()
	payl, err := protoutil.UnmarshalPayload(env.Payload)
	if err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	shdr, err := protoutil.UnmarshalSignatureHeader(payl.Header.SignatureHeader)
	if err != nil {
		t.Fatalf("unmarshal signature header: %v", err)
	}
	return payl, shdr
}

// verifyEnvelope does what an orderer enforcing a Writers policy does: read the
// creator as an msppb.Identity, and check the envelope signature over the
// payload bytes against the certificate it carries. It does not evaluate a
// policy, so it does not check the certificate against an MSP's CA.
func verifyEnvelope(env *common.Envelope) error {
	payl, err := protoutil.UnmarshalPayload(env.Payload)
	if err != nil {
		return err
	}
	shdr, err := protoutil.UnmarshalSignatureHeader(payl.Header.SignatureHeader)
	if err != nil {
		return err
	}
	id, err := protoutil.UnmarshalIdentity(shdr.Creator)
	if err != nil {
		return err
	}
	cert, err := parseCert(id.GetCertificate())
	if err != nil {
		return err
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("creator certificate is not an ECDSA certificate")
	}
	digest := sha256.Sum256(env.Payload)
	if !ecdsa.VerifyASN1(pub, digest[:], env.Signature) {
		return errors.New("signature does not verify over the payload")
	}
	return nil
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, errors.New("creator is not a PEM certificate")
	}
	return x509.ParseCertificate(block.Bytes)
}

// TestCreateTx_Signed checks the wire format the router's signature filter
// reads: the creator must parse as an msppb.Identity, although the SDK's
// signers serialize an msp.SerializedIdentity, and the signature must verify
// over the payload bytes.
func TestCreateTx_Signed(t *testing.T) {
	client, certPEM := newMSPSigner(t, "Org1MSP")
	end := newEndorsement(t, client)

	env, err := CreateTx(end.Proposal, client, end.Responses...)
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}

	payl, shdr := unpack(t, env)
	id, err := protoutil.UnmarshalIdentity(shdr.Creator)
	if err != nil {
		t.Fatalf("creator does not parse as msppb.Identity: %v", err)
	}
	if id.MspId != "Org1MSP" {
		t.Errorf("unexpected MSP ID: %q", id.MspId)
	}
	if !bytes.Equal(id.GetCertificate(), certPEM) {
		t.Error("creator does not carry the signer's certificate")
	}
	if len(env.Signature) == 0 {
		t.Fatal("expected a signature")
	}
	if err := verifyEnvelope(env); err != nil {
		t.Errorf("envelope does not verify: %v", err)
	}

	// signing must not change what is being submitted
	chdr, err := protoutil.UnmarshalChannelHeader(payl.Header.ChannelHeader)
	if err != nil {
		t.Fatalf("unmarshal channel header: %v", err)
	}
	if common.HeaderType(chdr.Type) != common.HeaderType_MESSAGE {
		t.Errorf("unexpected header type: %s", common.HeaderType(chdr.Type))
	}
	unsigned, err := CreateTx(end.Proposal, nil, end.Responses...)
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	unsignedPayl, _ := unpack(t, unsigned)
	if !bytes.Equal(payl.Data, unsignedPayl.Data) {
		t.Error("signed and unsigned envelopes carry different transactions")
	}
}

// TestCreateTx_TamperedPayload guards verifyEnvelope itself, and with it
// the claim that the signature is over the payload bytes and not something else.
func TestCreateTx_TamperedPayload(t *testing.T) {
	client, _ := newMSPSigner(t, "Org1MSP")
	end := newEndorsement(t, client)

	env, err := CreateTx(end.Proposal, client, end.Responses...)
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	if err := verifyEnvelope(env); err != nil {
		t.Fatalf("untampered envelope does not verify: %v", err)
	}

	payl, _ := unpack(t, env)
	payl.Data = append(payl.Data, 0x00)
	env.Payload = protoutil.MarshalOrPanic(payl)
	if err := verifyEnvelope(env); err == nil {
		t.Error("tampered payload must not verify")
	}
}

// TestCreateTx_Unsigned pins the unsigned shape, which deployments whose
// orderer does not verify client signatures rely on.
func TestCreateTx_Unsigned(t *testing.T) {
	client, _ := newMSPSigner(t, "Org1MSP")
	end := newEndorsement(t, client)

	for name, create := range map[string]func() (*common.Envelope, error){
		"CreateTx":          func() (*common.Envelope, error) { return CreateTx(end.Proposal, nil, end.Responses...) },
		"NewTxPackager nil": func() (*common.Envelope, error) { return NewTxPackager(nil).PackageTx(end) },
		"zero TxPackager":   func() (*common.Envelope, error) { return TxPackager{}.PackageTx(end) },
	} {
		t.Run(name, func(t *testing.T) {
			env, err := create()
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if len(env.Signature) != 0 {
				t.Errorf("expected no signature, got %d bytes", len(env.Signature))
			}
			_, shdr := unpack(t, env)
			if len(shdr.Creator) != 0 {
				t.Errorf("expected no creator, got %d bytes", len(shdr.Creator))
			}
			if len(shdr.Nonce) != 0 {
				t.Errorf("expected no nonce either, got %d bytes", len(shdr.Nonce))
			}
		})
	}
}

func TestPackageTx_Signed(t *testing.T) {
	client, _ := newMSPSigner(t, "Org1MSP")
	end := newEndorsement(t, client)

	env, err := NewTxPackager(client).PackageTx(end)
	if err != nil {
		t.Fatalf("PackageTx: %v", err)
	}
	if err := verifyEnvelope(env); err != nil {
		t.Errorf("envelope does not verify: %v", err)
	}
}

// brokenSigner has the client's identity, but cannot sign.
type brokenSigner struct{ sdk.Signer }

func (brokenSigner) Sign([]byte) ([]byte, error) { return nil, errors.New("hsm unavailable") }

type noIdentitySigner struct{}

func (noIdentitySigner) Sign([]byte) ([]byte, error) { return []byte("sig"), nil }
func (noIdentitySigner) Serialize() ([]byte, error)  { return nil, errors.New("no identity") }

// TestCreateTx_SignerErrors: signer.Serialize() builds the envelope's own creator now, so a
// serialize failure surfaces here just as much as a sign failure does.
func TestCreateTx_SignerErrors(t *testing.T) {
	client, _ := newMSPSigner(t, "Org1MSP")
	end := newEndorsement(t, client)

	if _, err := CreateTx(end.Proposal, brokenSigner{client}, end.Responses...); err == nil || !strings.Contains(err.Error(), "hsm unavailable") {
		t.Errorf("expected the signing error, got %v", err)
	}
	if _, err := CreateTx(end.Proposal, noIdentitySigner{}, end.Responses...); err == nil || !strings.Contains(err.Error(), "no identity") {
		t.Errorf("expected the serialize error, got %v", err)
	}
}

// TestCreateTx_NeedsResponses keeps the input validation of CreateTx on
// the signed path too.
func TestCreateTx_NeedsResponses(t *testing.T) {
	client, _ := newMSPSigner(t, "Org1MSP")
	end := newEndorsement(t, client)

	if _, err := CreateTx(end.Proposal, client); err == nil {
		t.Error("expected an error without proposal responses")
	}
}
