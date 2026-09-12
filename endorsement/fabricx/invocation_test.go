/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	commonpb "github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	"github.com/hyperledger/fabric-x-common/api/msppb"
	"github.com/hyperledger/fabric-x-common/protoutil"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/endorsement"
	"github.com/hyperledger/fabric-x-sdk/fabrictest"
	networkfabricx "github.com/hyperledger/fabric-x-sdk/network/fabricx"
	"google.golang.org/protobuf/proto"
)

func newInvocation(t *testing.T) endorsement.Invocation {
	t.Helper()
	inv, err := NewInvocation(fixedSigner{}, "mychannel", "myns", "v1", [][]byte{[]byte("fn"), []byte("arg")})
	if err != nil {
		t.Fatalf("NewInvocation failed: %v", err)
	}
	return inv
}

// headers unpacks the header the invocation carries on its proposal.
func headers(t *testing.T, inv endorsement.Invocation) (*commonpb.ChannelHeader, *commonpb.SignatureHeader) {
	t.Helper()
	if inv.Proposal == nil {
		t.Fatal("Proposal must not be nil")
	}
	hdr, err := protoutil.UnmarshalHeader(inv.Proposal.Header)
	if err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	chdr, err := protoutil.UnmarshalChannelHeader(hdr.ChannelHeader)
	if err != nil {
		t.Fatalf("unmarshal channel header: %v", err)
	}
	shdr, err := protoutil.UnmarshalSignatureHeader(hdr.SignatureHeader)
	if err != nil {
		t.Fatalf("unmarshal signature header: %v", err)
	}
	return chdr, shdr
}

// TestNewInvocation_CarriesHeaderOnly is the point of this constructor: a
// Fabric-X envelope never reads the proposal payload or the proposal hash, so
// neither is built.
func TestNewInvocation_CarriesHeaderOnly(t *testing.T) {
	inv := newInvocation(t)
	if len(inv.Proposal.Payload) != 0 {
		t.Errorf("expected no proposal payload, got %d bytes", len(inv.Proposal.Payload))
	}
	if len(inv.ProposalHash) != 0 {
		t.Errorf("expected no proposal hash, got %d bytes", len(inv.ProposalHash))
	}
}

// TestNewInvocation_NoChaincodeHeaderExtension guards the other omission:
// nothing on the Fabric-X path reads the chaincode header extension.
func TestNewInvocation_NoChaincodeHeaderExtension(t *testing.T) {
	chdr, _ := headers(t, newInvocation(t))
	if len(chdr.Extension) != 0 {
		t.Errorf("expected no channel header extension, got %d bytes", len(chdr.Extension))
	}
}

func TestNewInvocation_HeaderFields(t *testing.T) {
	chdr, _ := headers(t, newInvocation(t))
	if commonpb.HeaderType(chdr.Type) != commonpb.HeaderType_ENDORSER_TRANSACTION {
		t.Errorf("unexpected header type: %s", commonpb.HeaderType(chdr.Type))
	}
	if chdr.ChannelId != "mychannel" {
		t.Errorf("unexpected channel id: %q", chdr.ChannelId)
	}
	if chdr.Timestamp == nil {
		t.Error("timestamp must be set, endorsement.Parse rejects a proposal without one")
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

	chdr, shdr := headers(t, inv)
	if chdr.TxId != inv.TxID {
		t.Errorf("channel header tx id %q does not match invocation %q", chdr.TxId, inv.TxID)
	}
	if !bytes.Equal(shdr.Nonce, inv.Nonce) {
		t.Errorf("signature header nonce does not round-trip: got %x, want %x", shdr.Nonce, inv.Nonce)
	}
	if !bytes.Equal(shdr.Creator, inv.Creator) {
		t.Errorf("signature header creator does not round-trip: got %q, want %q", shdr.Creator, inv.Creator)
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
	if inv.CCID == nil || inv.CCID.Name != "myns" || inv.CCID.Version != "v1" {
		t.Errorf("unexpected chaincode id: %+v", inv.CCID)
	}
	if inv.Channel != "mychannel" {
		t.Errorf("unexpected channel: %q", inv.Channel)
	}
	if len(inv.Args) != 2 || string(inv.Args[0]) != "fn" || string(inv.Args[1]) != "arg" {
		t.Errorf("args do not round-trip: %q", inv.Args)
	}
}

// failingSigner cannot produce an identity.
type failingSigner struct{}

func (failingSigner) Sign(_ []byte) ([]byte, error) { return nil, errors.New("sign") }
func (failingSigner) Serialize() ([]byte, error)    { return nil, errors.New("no identity") }

func TestNewInvocation_SerializeError(t *testing.T) {
	_, err := NewInvocation(failingSigner{}, "mychannel", "myns", "v1", nil)
	if err == nil {
		t.Fatal("expected an error when the signer cannot serialize")
	}
	if err.Error() != "no identity" {
		t.Errorf("expected the signer error to be returned unwrapped, got %v", err)
	}
}

// TestNewInvocation_SufficientForPackaging is the test that makes the omissions
// above safe rather than merely intentional: it runs a header-only invocation
// all the way through the Fabric-X packager, which is the only consumer of
// Invocation.Proposal on this path. It reads the header and nothing else, so a
// proposal without payload, hash or extension still produces a valid envelope.
func TestNewInvocation_SufficientForPackaging(t *testing.T) {
	inv := newInvocation(t)

	res := endorsement.Success(blocks.ReadWriteSet{
		Writes: []blocks.KVWrite{{Key: "a", Value: []byte("va")}},
	}, nil, nil)
	resp, err := NewEndorsementBuilder(fabrictest.MockSigner{}).Endorse(inv, res)
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}

	env, err := networkfabricx.CreateTx(inv.Proposal, resp)
	if err != nil {
		t.Fatalf("CreateTx failed on a header-only proposal: %v", err)
	}

	payload, err := protoutil.UnmarshalPayload(env.Payload)
	if err != nil {
		t.Fatalf("unmarshal envelope payload: %v", err)
	}
	chdr, err := protoutil.UnmarshalChannelHeader(payload.Header.ChannelHeader)
	if err != nil {
		t.Fatalf("unmarshal channel header: %v", err)
	}
	if chdr.TxId != inv.TxID {
		t.Errorf("envelope tx id %q does not match invocation %q", chdr.TxId, inv.TxID)
	}
	if chdr.ChannelId != "mychannel" {
		t.Errorf("unexpected envelope channel id: %q", chdr.ChannelId)
	}
	if commonpb.HeaderType(chdr.Type) != commonpb.HeaderType_MESSAGE {
		t.Errorf("expected the packager to rewrite the type to MESSAGE, got %s", commonpb.HeaderType(chdr.Type))
	}
}

// namedSigner serialises a distinct identity per organisation, so several of
// them can endorse one transaction the way separate orgs do on a real network.
type namedSigner struct{ mspID string }

func (s namedSigner) Sign(digest []byte) ([]byte, error) {
	return append([]byte(s.mspID+":"), digest...), nil
}

func (s namedSigner) Serialize() ([]byte, error) {
	return proto.Marshal(&msppb.Identity{MspId: s.mspID})
}

// endorseWith runs one endorser over a fixed execution result.
func endorseWith(t *testing.T, mspID string, inv endorsement.Invocation) *peer.ProposalResponse {
	t.Helper()
	res := endorsement.Success(blocks.ReadWriteSet{
		Reads:  []blocks.KVRead{{Key: "r", Version: &blocks.Version{BlockNum: 3}}},
		Writes: []blocks.KVWrite{{Key: "w", Value: []byte("v")}},
	}, []byte("event"), nil)
	resp, err := NewEndorsementBuilder(namedSigner{mspID}).Endorse(inv, res)
	if err != nil {
		t.Fatalf("Endorse as %s failed: %v", mspID, err)
	}
	return resp
}

// TestNewInvocation_TwoOfTwo is the scenario that matters most in practice.
// CreateTx requires every endorser's ProposalResponsePayload to be bitwise
// equal, so an invocation that fed any per-endorser state into the payload
// would pass single-endorser tests and fail a 2-of-2 policy on a real network.
func TestNewInvocation_TwoOfTwo(t *testing.T) {
	inv := newInvocation(t)
	org0 := endorseWith(t, "peer-org-0", inv)
	org1 := endorseWith(t, "peer-org-1", inv)

	if !bytes.Equal(org0.Payload, org1.Payload) {
		t.Fatalf("endorsers disagree on the payload:\n org0 %x\n org1 %x", org0.Payload, org1.Payload)
	}

	env, err := networkfabricx.CreateTx(inv.Proposal, org0, org1)
	if err != nil {
		t.Fatalf("CreateTx with two endorsements failed: %v", err)
	}

	payload, err := protoutil.UnmarshalPayload(env.Payload)
	if err != nil {
		t.Fatalf("unmarshal envelope payload: %v", err)
	}
	var tx applicationpb.Tx
	if err := proto.Unmarshal(payload.Data, &tx); err != nil {
		t.Fatalf("unmarshal Tx: %v", err)
	}
	if len(tx.Endorsements) != 1 {
		t.Fatalf("expected endorsements for 1 namespace, got %d", len(tx.Endorsements))
	}
	if got := len(tx.Endorsements[0].EndorsementsWithIdentity); got != 2 {
		t.Fatalf("expected 2 endorsements to survive packaging, got %d", got)
	}
	for i, e := range tx.Endorsements[0].EndorsementsWithIdentity {
		if e.Identity == nil || e.Identity.MspId == "" {
			t.Errorf("endorsement[%d] lost its identity: %+v", i, e.Identity)
		}
	}
}

// TestNewInvocation_DivergentEndorsersRejected is the negative half of the
// 2-of-2 case: endorsers that executed to different read-write sets must not
// package into one transaction.
func TestNewInvocation_DivergentEndorsersRejected(t *testing.T) {
	inv := newInvocation(t)
	org0 := endorseWith(t, "peer-org-0", inv)

	diverged, err := NewEndorsementBuilder(namedSigner{"peer-org-1"}).Endorse(inv, endorsement.Success(
		blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "w", Value: []byte("different")}}}, nil, nil))
	if err != nil {
		t.Fatalf("Endorse failed: %v", err)
	}

	if _, err := networkfabricx.CreateTx(inv.Proposal, org0, diverged); err == nil {
		t.Fatal("expected CreateTx to reject endorsers that disagree on the payload")
	}
}

// TestNewInvocation_NotForRemoteEndorsers pins down the boundary of this
// constructor. endorsement.Parse reads the proposal payload, which a header-only
// invocation does not carry, so this cannot be used to build a SignedProposal
// for an endorser that parses one. It is for the local submit path, where the
// Invocation is handed to the builder directly.
func TestNewInvocation_NotForRemoteEndorsers(t *testing.T) {
	inv := newInvocation(t)
	signed, err := protoutil.GetSignedProposal(inv.Proposal, fixedSigner{})
	if err != nil {
		t.Fatalf("GetSignedProposal: %v", err)
	}
	if _, err := endorsement.Parse(signed, time.Now()); err == nil {
		t.Fatal("expected Parse to reject a proposal with no payload; " +
			"if this now passes, the doc comment on NewInvocation needs updating")
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
			inv, err := NewInvocation(fixedSigner{}, tt.channel, tt.namespace, tt.nsVersion, tt.args)
			if err != nil {
				t.Fatalf("NewInvocation: %v", err)
			}
			if inv.TxID == "" {
				t.Error("tx id must be set even for empty inputs")
			}
			if inv.CCID == nil {
				t.Fatal("CCID must never be nil, the builder dereferences it")
			}

			// The builder must survive it too: CCID.Name feeds the namespace and
			// the event, and empty args must still produce two metadata entries.
			resp, err := NewEndorsementBuilder(fabrictest.MockSigner{}).Endorse(inv, endorsement.Success(
				blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "k", Value: []byte("v")}}}, nil, nil))
			if err != nil {
				t.Fatalf("Endorse: %v", err)
			}
			if _, err := networkfabricx.CreateTx(inv.Proposal, resp); err != nil {
				t.Fatalf("CreateTx: %v", err)
			}
		})
	}
}

func TestNewInvocation_LongAndUnicodeNamespace(t *testing.T) {
	ns := strings.Repeat("ünïcödé-ns-", 40)
	inv, err := NewInvocation(fixedSigner{}, "채널", ns, "v1", [][]byte{[]byte("大きい")})
	if err != nil {
		t.Fatalf("NewInvocation: %v", err)
	}
	if inv.CCID.Name != ns {
		t.Error("namespace does not round-trip")
	}
	chdr, _ := headers(t, inv)
	if chdr.ChannelId != "채널" {
		t.Errorf("channel does not round-trip: %q", chdr.ChannelId)
	}
}

// TestNewInvocation_ConcurrentUniqueness runs under -race and guards the nonce
// source: a shared or seeded generator would show up here as a collision.
func TestNewInvocation_ConcurrentUniqueness(t *testing.T) {
	const n = 200

	var wg sync.WaitGroup
	ids := make([]string, n)
	nonces := make([][]byte, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inv, err := NewInvocation(fixedSigner{}, "ch", "ns", "v1", nil)
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

// TestNewInvocation_TimestampIsCurrent matters because endorsement.Parse
// rejects a proposal whose timestamp is more than five minutes from the
// endorser's clock.
func TestNewInvocation_TimestampIsCurrent(t *testing.T) {
	before := time.Now().Add(-time.Second)
	chdr, _ := headers(t, newInvocation(t))
	after := time.Now().Add(time.Second)

	ts := chdr.Timestamp.AsTime()
	if ts.Before(before) || ts.After(after) {
		t.Errorf("timestamp %v is not current (expected between %v and %v)", ts, before, after)
	}
}

// TestNewInvocation_MustBeSharedAcrossEndorsers pins down a trap that a
// single-endorser test cannot see. The transaction id reaches the signed digest
// directly, but reaches the payload only through the chaincode event, so two
// endorsers that each built their own invocation produce matching payloads when
// there is no event, pass CreateTx's equality check, and are only rejected later
// by the committer verifying signatures over the envelope's single tx id.
func TestNewInvocation_MustBeSharedAcrossEndorsers(t *testing.T) {
	own0, own1 := newInvocation(t), newInvocation(t)
	if own0.TxID == own1.TxID {
		t.Fatal("expected two invocations to differ")
	}

	rws := blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "w", Value: []byte("v")}}}
	digest := func(inv endorsement.Invocation, mspID string, event []byte) []byte {
		t.Helper()
		// namedSigner returns mspID + ":" + digest, so the prefix comes back off.
		resp, err := NewEndorsementBuilder(namedSigner{mspID}).Endorse(inv, endorsement.Success(rws, event, nil))
		if err != nil {
			t.Fatalf("Endorse: %v", err)
		}
		return resp.Endorsement.Signature[len(mspID)+1:]
	}

	if bytes.Equal(digest(own0, "org0", nil), digest(own1, "org1", nil)) {
		t.Error("expected separate invocations to be signed over different digests")
	}
	// Sharing one invocation is what makes the two endorsements agree.
	if !bytes.Equal(digest(own0, "org0", nil), digest(own0, "org1", nil)) {
		t.Error("one invocation must produce one digest regardless of endorser")
	}

	// The packager only catches the divergence when an event carries the tx id
	// into the payload; without one the payloads match and it cannot.
	withoutEvent := []*peer.ProposalResponse{endorseNoEvent(t, "org0", own0), endorseNoEvent(t, "org1", own1)}
	if _, err := networkfabricx.CreateTx(own0.Proposal, withoutEvent...); err != nil {
		t.Logf("packager rejected mismatched invocations without an event: %v"+
			" (it no longer has the blind spot this test documents)", err)
	}
}

func endorseNoEvent(t *testing.T, mspID string, inv endorsement.Invocation) *peer.ProposalResponse {
	t.Helper()
	resp, err := NewEndorsementBuilder(namedSigner{mspID}).Endorse(inv, endorsement.Success(
		blocks.ReadWriteSet{Writes: []blocks.KVWrite{{Key: "w", Value: []byte("v")}}}, nil, nil))
	if err != nil {
		t.Fatalf("Endorse: %v", err)
	}
	return resp
}
