/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package identity has the bare minimum for Fabric ecdsa identities.
package identity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyperledger/fabric-lib-go/bccsp/utils"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"github.com/hyperledger/fabric-x-common/api/msppb"
	"google.golang.org/protobuf/proto"
)

// Signer is an ECDSA fabric identity.
type Signer struct {
	priv_sk    *ecdsa.PrivateKey
	signcert   []byte
	mspID      string
	serialized []byte
}

// SignerFromMSP creates a standard Fabric identity from an msp folder.
func SignerFromMSP(dir, mspID string) (Signer, error) {
	keyFiles, err := filepath.Glob(filepath.Join(dir, "keystore", "*_sk"))
	if err != nil || len(keyFiles) == 0 {
		// fabric-ca names the key priv-key.pem instead of *_sk
		keyFiles, err = filepath.Glob(filepath.Join(dir, "keystore", "*.pem"))
	}
	if err != nil || len(keyFiles) == 0 {
		return Signer{}, fmt.Errorf("no private key found in path %s: %w", filepath.Join(dir, "keystore"), err)
	}
	privBytes, err := os.ReadFile(keyFiles[0])
	if err != nil {
		return Signer{}, err
	}
	pk, err := parsePrivateKey(privBytes)
	if err != nil {
		return Signer{}, err
	}

	certFiles, err := filepath.Glob(filepath.Join(dir, "signcerts", "*.pem"))
	if err != nil || len(certFiles) == 0 {
		return Signer{}, fmt.Errorf("no signcert found: %w", err)
	}
	certPEM, err := os.ReadFile(certFiles[0])
	if err != nil {
		return Signer{}, err
	}
	serialized, err := proto.Marshal(&msp.SerializedIdentity{Mspid: mspID, IdBytes: certPEM})
	if err != nil {
		return Signer{}, fmt.Errorf("serialize certificate identity: %w", err)
	}

	return Signer{
		priv_sk:    pk,
		signcert:   certPEM,
		mspID:      mspID,
		serialized: serialized,
	}, nil
}

// Sign hashes msg with SHA-256 and returns a low-S DER-encoded ECDSA signature.
func (s Signer) Sign(msg []byte) ([]byte, error) {
	return sign(s.priv_sk, msg)
}

func sign(k *ecdsa.PrivateKey, message []byte) ([]byte, error) {
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, k, digest[:])
	if err != nil {
		return nil, err
	}

	s, err = utils.ToLowS(&k.PublicKey, s)
	if err != nil {
		return nil, err
	}

	return utils.MarshalECDSASignature(r, s)
}

// WithCertificateHash returns a copy using the lowercase hex SHA-256 of the
// sign certificate's DER encoding as its certificate ID, for Fabric-X consumers
// supporting cached identities. It does not hash the PEM text or normalize the
// certificate. The original signer and signature generation are unchanged.
//
// The certificate must already be registered in the channel MSP's known_certs.
// This default matches MSPs using SHA-256 identity identifiers and certificates
// with low-S ECDSA signatures, such as those produced by configtxgen or fabric-ca.
// For a different MSP hash configuration or a high-S certificate that the MSP
// normalizes, use WithCertificateID with the MSP's registered identifier instead.
// Registration and correspondence to the signing key are checked by the receiver.
func (s Signer) WithCertificateHash() (Signer, error) {
	block, _ := pem.Decode(s.signcert)
	if block == nil || block.Type != "CERTIFICATE" {
		return Signer{}, fmt.Errorf("failed to decode PEM sign certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return Signer{}, fmt.Errorf("parse sign certificate: %w", err)
	}
	digest := sha256.Sum256(block.Bytes)
	return s.WithCertificateID(hex.EncodeToString(digest[:]))
}

// WithCertificateID returns a copy that serializes a registered certificate ID
// instead of the full certificate, for Fabric-X consumers supporting cached
// identities. The original signer and signature generation are unchanged.
//
// certificateID must be the identity identifier assigned by the channel MSP to
// this signer's certificate, which must already be registered in its known_certs
// configuration. Hash configuration and certificate normalization can affect that
// identifier. This method rejects empty IDs and returns serialization errors;
// registration and correspondence to the signing key are checked by the receiving
// MSP, not by this method.
func (s Signer) WithCertificateID(certificateID string) (Signer, error) {
	if certificateID == "" {
		return Signer{}, fmt.Errorf("certificate ID must not be empty")
	}
	serialized, err := proto.Marshal(msppb.NewIdentityWithIDOfCert(s.mspID, certificateID))
	if err != nil {
		return Signer{}, fmt.Errorf("serialize certificate ID identity: %w", err)
	}
	s.serialized = serialized
	return s, nil
}

// Serialize returns a copy of the cached MSP identity: the full certificate by
// default, or the ID configured by WithCertificateHash or WithCertificateID.
// Serialization errors are returned when constructing or configuring the signer;
// this method retains the standard Fabric signer signature and returns a nil error.
func (s Signer) Serialize() ([]byte, error) {
	return bytes.Clone(s.serialized), nil
}

func parsePrivateKey(privPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(privPEM)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM private key")
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse pkcs8 private key: %w", err)
	}
	pk, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not an ECDSA private key")
	}
	return pk, nil
}
