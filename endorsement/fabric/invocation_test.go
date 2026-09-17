/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	commonpb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
)

func newInvocation(t *testing.T) endorsement.Invocation {
	t.Helper()
	inv, err := NewInvocationBuilder(fixedSigner{}).NewInvocation("mychannel", "myns", "v1", 0, [][]byte{[]byte("fn"), []byte("arg")})
	if err != nil {
		t.Fatalf("NewInvocation failed: %v", err)
	}
	return inv
}

func TestNewInvocation_CarriesFullProposal(t *testing.T) {
	inv := newInvocation(t)
	if len(inv.Proposal.Payload) == 0 {
		t.Error("expected a proposal payload; the Fabric peer reads it")
	}
	if len(inv.ProposalHash) == 0 {
		t.Error("expected a proposal hash; it is part of the Fabric endorsement")
	}

	hdr, err := protoutil.UnmarshalHeader(inv.Proposal.Header)
	if err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	chdr, err := protoutil.UnmarshalChannelHeader(hdr.ChannelHeader)
	if err != nil {
		t.Fatalf("unmarshal channel header: %v", err)
	}
	if len(chdr.Extension) == 0 {
		t.Error("expected a chaincode header extension")
	}
	if commonpb.HeaderType(chdr.Type) != commonpb.HeaderType_ENDORSER_TRANSACTION {
		t.Errorf("unexpected header type: %s", commonpb.HeaderType(chdr.Type))
	}
	if chdr.ChannelId != "mychannel" {
		t.Errorf("unexpected channel id: %q", chdr.ChannelId)
	}
}

func TestNewInvocation_TxIDDerivedFromNonceAndCreator(t *testing.T) {
	inv := newInvocation(t)
	if len(inv.Nonce) != nonceSize {
		t.Errorf("expected a %d byte nonce, got %d", nonceSize, len(inv.Nonce))
	}
	if want := protoutil.ComputeTxID(inv.Nonce, inv.Creator); inv.TxID != want {
		t.Errorf("tx id not derived from nonce and creator: got %q, want %q", inv.TxID, want)
	}
	if !bytes.Equal(inv.Creator, []byte("identity")) {
		t.Errorf("unexpected creator: %q", inv.Creator)
	}
}

func TestNewInvocation_NonceIsFresh(t *testing.T) {
	first, second := newInvocation(t), newInvocation(t)
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Error("two invocations share a nonce")
	}
	if first.TxID == second.TxID {
		t.Error("two invocations share a tx id")
	}
}

func TestNewInvocation_CarriesNamespaceAndArgs(t *testing.T) {
	inv := newInvocation(t)
	if inv.Namespace != "myns" || inv.ChaincodeVersion != "v1" {
		t.Errorf("unexpected namespace/version: %+v", inv)
	}
	if inv.Channel != "mychannel" {
		t.Errorf("unexpected channel: %q", inv.Channel)
	}
	if len(inv.Args) != 2 || string(inv.Args[0]) != "fn" || string(inv.Args[1]) != "arg" {
		t.Errorf("args do not round-trip: %q", inv.Args)
	}
}

type failingSigner struct{}

func (failingSigner) Sign(_ []byte) ([]byte, error) { return nil, errors.New("sign") }
func (failingSigner) Serialize() ([]byte, error)    { return nil, errors.New("no identity") }

func TestNewInvocation_SerializeError(t *testing.T) {
	_, err := NewInvocationBuilder(failingSigner{}).NewInvocation("mychannel", "myns", "v1", 0, nil)
	if err == nil {
		t.Fatal("expected an error when the signer cannot serialize")
	}
	if err.Error() != "no identity" {
		t.Errorf("expected the signer error to be returned unwrapped, got %v", err)
	}
}

func TestNewInvocation_ParseableByEndorser(t *testing.T) {
	inv := newInvocation(t)
	signed, err := protoutil.GetSignedProposal(inv.Proposal, fixedSigner{})
	if err != nil {
		t.Fatalf("GetSignedProposal: %v", err)
	}
	parsed, err := Parse(signed, time.Now())
	if err != nil {
		t.Fatalf("Parse rejected a full proposal: %v", err)
	}
	if parsed.TxID != inv.TxID {
		t.Errorf("parsed tx id %q does not match %q", parsed.TxID, inv.TxID)
	}
	if parsed.Channel != inv.Channel {
		t.Errorf("parsed channel %q does not match %q", parsed.Channel, inv.Channel)
	}
	if len(parsed.Args) != 2 || string(parsed.Args[0]) != "fn" || string(parsed.Args[1]) != "arg" {
		t.Errorf("parsed args do not match: %q", parsed.Args)
	}
}

func TestNewInvocation_NilSigner(t *testing.T) {
	for _, b := range []InvocationBuilder{NewInvocationBuilder(nil), {}} {
		_, err := b.NewInvocation("mychannel", "myns", "v1", 0, nil)
		if err == nil {
			t.Fatal("expected an error for a nil signer")
		}
		if err.Error() != "nil signer" {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestNewInvocation_AsInterface(t *testing.T) {
	var b endorsement.InvocationBuilder = NewInvocationBuilder(fixedSigner{})
	inv, err := b.NewInvocation("mychannel", "myns", "v1", 0, [][]byte{[]byte("fn")})
	if err != nil {
		t.Fatalf("NewInvocation: %v", err)
	}
	if inv.TxID == "" || inv.Proposal == nil || len(inv.ProposalHash) == 0 {
		t.Fatalf("interface call produced an incomplete invocation: %+v", inv)
	}
}

func TestNewInvocation_SufficientForEndorse(t *testing.T) {
	inv := newInvocation(t)
	resp, err := NewEndorsementBuilder(fixedSigner{}).Endorse(inv, endorsement.Success(
		blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v")}}}, nil, nil))
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}
	if resp.Endorsement == nil || len(resp.Payload) == 0 {
		t.Fatal("expected a signed proposal response")
	}
}

func TestNewInvocation_EmptyInputs(t *testing.T) {
	tests := []struct {
		name                          string
		channel, namespace, nsVersion string
		args                          [][]byte
	}{
		{name: "nil args", channel: "ch", namespace: "ns", nsVersion: "v1", args: nil},
		{name: "empty args", channel: "ch", namespace: "ns", nsVersion: "v1", args: [][]byte{}},
		{name: "empty arg entry", channel: "ch", namespace: "ns", nsVersion: "v1", args: [][]byte{{}}},
		{name: "no channel", channel: "", namespace: "ns", nsVersion: "v1", args: [][]byte{[]byte("a")}},
		{name: "no namespace", channel: "ch", namespace: "", nsVersion: "v1", args: [][]byte{[]byte("a")}},
		{name: "no version", channel: "ch", namespace: "ns", nsVersion: "", args: [][]byte{[]byte("a")}},
		{name: "all empty", channel: "", namespace: "", nsVersion: "", args: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inv, err := NewInvocationBuilder(fixedSigner{}).NewInvocation(tt.channel, tt.namespace, tt.nsVersion, 0, tt.args)
			if err != nil {
				t.Fatalf("NewInvocation: %v", err)
			}
			if inv.TxID == "" {
				t.Error("tx id must be set even for empty inputs")
			}
			if _, err := NewEndorsementBuilder(fixedSigner{}).Endorse(inv, endorsement.Success(
				blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v")}}}, nil, nil)); err != nil {
				t.Fatalf("Endorse: %v", err)
			}
		})
	}
}

func TestNewInvocation_ConcurrentUniqueness(t *testing.T) {
	const n = 200

	var wg sync.WaitGroup
	ids := make([]string, n)
	nonces := make([][]byte, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inv, err := NewInvocationBuilder(fixedSigner{}).NewInvocation("ch", "ns", "v1", 0, nil)
			if err != nil {
				t.Errorf("NewInvocation: %v", err)
				return
			}
			ids[i], nonces[i] = inv.TxID, inv.Nonce
		}()
	}
	wg.Wait()

	seenID := make(map[string]struct{}, n)
	seenNonce := make(map[string]struct{}, n)
	for i := range n {
		if _, dup := seenID[ids[i]]; dup {
			t.Fatalf("duplicate tx id at %d: %s", i, ids[i])
		}
		if _, dup := seenNonce[string(nonces[i])]; dup {
			t.Fatalf("duplicate nonce at %d", i)
		}
		seenID[ids[i]] = struct{}{}
		seenNonce[string(nonces[i])] = struct{}{}
	}
}
