/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package identity

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	fabricmsp "github.com/hyperledger/fabric-protos-go-apiv2/msp"
	"github.com/hyperledger/fabric-x-common/api/msppb"
	"github.com/hyperledger/fabric-x-common/msp"
	"google.golang.org/protobuf/proto"
)

func TestSignerWithCertificateID(t *testing.T) {
	_, keyPEM, certPEM := newTestECDSAIdentity(t)
	signer, err := SignerFromMSP(writeTestMSP(t, "identity_sk", keyPEM, certPEM), "Org1MSP")
	if err != nil {
		t.Fatal(err)
	}
	// IDs are supplied by the MSP; do not infer a hash algorithm or rewrite them.
	const certificateID = "registered-ID"
	compact, err := signer.WithCertificateID(certificateID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.WithCertificateID(""); err == nil {
		t.Fatal("empty certificate ID accepted")
	}
	if _, err := compact.WithCertificateID(""); err == nil {
		t.Fatal("empty certificate ID accepted on configured signer")
	}
	fullBytes, err := proto.Marshal(&fabricmsp.SerializedIdentity{Mspid: "Org1MSP", IdBytes: certPEM})
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]Signer{"original": signer, "compact": compact} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 8; i++ {
				t.Run("concurrent", func(t *testing.T) {
					t.Parallel()
					raw, err := s.Serialize()
					if err != nil {
						t.Fatal(err)
					}
					if name == "original" {
						if !bytes.Equal(raw, fullBytes) {
							t.Fatal("default serialization changed")
						}
					} else {
						var id msppb.Identity
						if err := proto.Unmarshal(raw, &id); err != nil {
							t.Fatal(err)
						}
						if _, ok := id.Creator.(*msppb.Identity_CertificateId); !ok || id.GetCertificateId() != certificateID || id.MspId != "Org1MSP" || len(id.GetCertificate()) != 0 {
							t.Fatalf("unexpected compact identity: %v", &id)
						}
					}
					message := []byte("unchanged signing behavior")
					sig, err := s.Sign(message)
					if err != nil {
						t.Fatal(err)
					}
					digest := sha256.Sum256(message)
					var parsed ecdsaSignature
					if _, err := asn1.Unmarshal(sig, &parsed); err != nil {
						t.Fatal(err)
					}
					if !ecdsa.Verify(&signer.priv_sk.PublicKey, digest[:], parsed.R, parsed.S) || parsed.S.Cmp(new(big.Int).Rsh(new(big.Int).Set(signer.priv_sk.Params().N), 1)) > 0 {
						t.Fatal("signature is invalid or not low-S")
					}
				})
			}
		})
	}
}

// A certificate's ECDSA signature is part of its DER encoding. The MSP
// normalizes high-S signatures before assigning the registered identity ID.
func TestSignerCertificateIDMSPLookup(t *testing.T) {
	for _, highS := range []bool{false, true} {
		name := "low-S"
		if highS {
			name = "high-S"
		}
		t.Run(name, func(t *testing.T) {
			caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ca := &x509.Certificate{
				SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CA"},
				NotBefore: time.Unix(0, 0), NotAfter: time.Unix(4102444800, 0),
				IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
			}
			caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
			if err != nil {
				t.Fatal(err)
			}
			key, keyPEM, _ := newTestECDSAIdentity(t)
			leaf := &x509.Certificate{
				SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "endorser"},
				NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
				BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
			}
			der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
			if err != nil {
				t.Fatal(err)
			}
			der = certificateWithSignatureS(t, der, caKey, highS)
			certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
			dir := writeTestMSP(t, "identity_sk", keyPEM, certPEM)
			writeTestFile(t, filepath.Join(dir, "cacerts", "ca.pem"), caPEM)
			writeTestFile(t, filepath.Join(dir, "admincerts", "admin.pem"), certPEM)
			writeTestFile(t, filepath.Join(dir, "knowncerts", "endorser.pem"), certPEM)
			writeTestFile(t, filepath.Join(dir, "knowncerts", "ca.pem"), caPEM)
			store, err := msp.LoadVerifyingMspDir(msp.DirLoadParameters{MspDir: dir, MspName: "Org1MSP"})
			if err != nil {
				t.Fatal(err)
			}
			registered, err := store.DeserializeIdentity(msppb.NewIdentity("Org1MSP", certPEM))
			if err != nil {
				t.Fatal(err)
			}
			if err := registered.Validate(); err != nil {
				t.Fatal(err)
			}
			rawDigest := sha256.Sum256(der)
			if (registered.GetIdentifier().Id != hex.EncodeToString(rawDigest[:])) != highS {
				t.Fatal("fixture did not exercise the expected MSP certificate normalization")
			}
			signer, err := SignerFromMSP(dir, "Org1MSP")
			if err != nil {
				t.Fatal(err)
			}
			compact, err := signer.WithCertificateID(registered.GetIdentifier().Id)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := compact.Serialize()
			if err != nil {
				t.Fatal(err)
			}
			var id msppb.Identity
			if err := proto.Unmarshal(raw, &id); err != nil {
				t.Fatal(err)
			}
			resolved, err := store.DeserializeIdentity(&id)
			if err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(); err != nil {
				t.Fatal(err)
			}
			message := []byte("registered endorsement")
			sig, err := compact.Sign(message)
			if err != nil {
				t.Fatal(err)
			}
			if err := resolved.Verify(message, sig); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DeserializeIdentity(msppb.NewIdentityWithIDOfCert("Org1MSP", "unregistered")); err == nil {
				t.Fatal("unregistered identity resolved")
			}
			other, err := store.DeserializeIdentity(msppb.NewIdentity("Org1MSP", caPEM))
			if err != nil {
				t.Fatal(err)
			}
			wrongSigner, err := signer.WithCertificateID(other.GetIdentifier().Id)
			if err != nil {
				t.Fatal(err)
			}
			wrongBytes, err := wrongSigner.Serialize()
			if err != nil {
				t.Fatal(err)
			}
			if err := proto.Unmarshal(wrongBytes, &id); err != nil {
				t.Fatal(err)
			}
			wrongIdentity, err := store.DeserializeIdentity(&id)
			if err != nil {
				t.Fatal(err)
			}
			if err := wrongIdentity.Verify(message, sig); err == nil {
				t.Fatal("signature verified with another registered certificate")
			}
		})
	}
}

func certificateWithSignatureS(t *testing.T, der []byte, issuer *ecdsa.PrivateKey, highS bool) []byte {
	t.Helper()
	var cert struct {
		TBS                asn1.RawValue
		SignatureAlgorithm pkix.AlgorithmIdentifier
		SignatureValue     asn1.BitString
	}
	if _, err := asn1.Unmarshal(der, &cert); err != nil {
		t.Fatal(err)
	}
	var sig ecdsaSignature
	if _, err := asn1.Unmarshal(cert.SignatureValue.Bytes, &sig); err != nil {
		t.Fatal(err)
	}
	half := new(big.Int).Rsh(new(big.Int).Set(issuer.Params().N), 1)
	if (sig.S.Cmp(half) > 0) != highS {
		sig.S.Sub(issuer.Params().N, sig.S)
	}
	encoded, err := asn1.Marshal(sig)
	if err != nil {
		t.Fatal(err)
	}
	cert.SignatureValue = asn1.BitString{Bytes: encoded, BitLength: len(encoded) * 8}
	result, err := asn1.Marshal(cert)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
