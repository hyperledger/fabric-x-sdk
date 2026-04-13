/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package identity has the bare minimum for Fabric ecdsa identities.
package identity

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyperledger/fabric-lib-go/bccsp/utils"
	"github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"google.golang.org/protobuf/proto"
)

// Signer is an ECDSA fabric identity.
type Signer struct {
	priv_sk  *ecdsa.PrivateKey
	signcert []byte
	mspID    string
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

	return Signer{
		priv_sk:  pk,
		signcert: certPEM,
		mspID:    mspID,
	}, nil
}

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

func (s Signer) Serialize() ([]byte, error) {
	return proto.Marshal(&msp.SerializedIdentity{Mspid: s.mspID, IdBytes: s.signcert})
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
