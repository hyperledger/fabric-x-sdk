/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	ordererpb "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"google.golang.org/grpc"
)

// --- fake orderer gRPC server ---

// fakeBroadcastServer is a minimal orderer.AtomicBroadcastServer that
// acknowledges every envelope it receives.
type fakeBroadcastServer struct {
	ordererpb.UnimplementedAtomicBroadcastServer

	mu       sync.Mutex
	received int
}

func (s *fakeBroadcastServer) Broadcast(stream ordererpb.AtomicBroadcast_BroadcastServer) error {
	for {
		env, err := stream.Recv()
		if err != nil {
			return nil //nolint:nilerr // client closing the stream is not an error here.
		}
		_ = env
		s.mu.Lock()
		s.received++
		s.mu.Unlock()
		if err := stream.Send(&ordererpb.BroadcastResponse{Status: common.Status_SUCCESS}); err != nil {
			return err
		}
	}
}

func (s *fakeBroadcastServer) receivedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.received
}

// startFakeOrderer starts a fake, acknowledging orderer on a random loopback
// port and returns its address. The server and listener are torn down via
// t.Cleanup.
func startFakeOrderer(t *testing.T) (string, *fakeBroadcastServer) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer()
	fake := &fakeBroadcastServer{}
	ordererpb.RegisterAtomicBroadcastServer(srv, fake)

	go srv.Serve(lis) //nolint:errcheck

	t.Cleanup(func() {
		srv.Stop()
	})

	return lis.Addr().String(), fake
}

// deadOrdererAddr returns a loopback address that is guaranteed to refuse
// connections: a listener is opened just to claim a free port, then closed
// immediately. Unlike a server that aborts the RPC after accepting it, a
// refused TCP connection reliably fails the very first Broadcast() call,
// since gRPC's default failfast behaviour surfaces a dial failure without
// waiting for the connection to become ready.
func deadOrdererAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	if err := lis.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

func testOrdererConf(addr string) OrdererConf {
	return OrdererConf{Address: addr, TLS: TLSConfig{Mode: TLSModeNone}}
}

// waitFor polls cond until it returns true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatal("condition not met before timeout")
	}
}

// --- test double for TxPackager ---

type mockPackager struct {
	mu       sync.Mutex
	err      error
	calls    int
	envelope *common.Envelope
}

func (m *mockPackager) PackageTx(_ sdk.Endorsement) (*common.Envelope, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if m.envelope != nil {
		return m.envelope, nil
	}
	return &common.Envelope{Payload: []byte("payload")}, nil
}

func (m *mockPackager) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// --- NewSubmitter ---

func TestNewSubmitter(t *testing.T) {
	t.Run("no orderers configured", func(t *testing.T) {
		s, err := NewSubmitter(t.Context(), nil, &mockPackager{}, 0, sdk.NoOpLogger{})
		if err == nil {
			t.Fatal("expected error")
		}
		if s != nil {
			t.Fatal("expected nil submitter on error")
		}
	})

	t.Run("invalid TLS config", func(t *testing.T) {
		cfg := []OrdererConf{{Address: "127.0.0.1:1234", TLS: TLSConfig{Mode: "bogus"}}}
		s, err := NewSubmitter(t.Context(), cfg, &mockPackager{}, 0, sdk.NoOpLogger{})
		if err == nil {
			t.Fatal("expected error")
		}
		if s != nil {
			t.Fatal("expected nil submitter on error")
		}
	})

	t.Run("address without port", func(t *testing.T) {
		cfg := []OrdererConf{{Address: "127.0.0.1", TLS: TLSConfig{Mode: TLSModeNone}}}
		s, err := NewSubmitter(t.Context(), cfg, &mockPackager{}, 0, sdk.NoOpLogger{})
		if err == nil {
			t.Fatal("expected error")
		}
		if s != nil {
			t.Fatal("expected nil submitter on error")
		}
	})

	t.Run("success", func(t *testing.T) {
		addr, _ := startFakeOrderer(t)
		s, err := NewSubmitter(t.Context(), []OrdererConf{testOrdererConf(addr)}, &mockPackager{}, 0, sdk.NoOpLogger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s == nil {
			t.Fatal("expected non-nil submitter")
		}
		t.Cleanup(func() { s.Close() }) //nolint:errcheck
	})
}

// --- Submit ---

func TestSubmitter_Submit(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		addr, fake := startFakeOrderer(t)
		pkg := &mockPackager{}
		s, err := NewSubmitter(t.Context(), []OrdererConf{testOrdererConf(addr)}, pkg, 0, sdk.NoOpLogger{})
		if err != nil {
			t.Fatalf("NewSubmitter: %v", err)
		}
		t.Cleanup(func() { s.Close() }) //nolint:errcheck

		if err := s.Submit(t.Context(), sdk.Endorsement{}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if pkg.callCount() != 1 {
			t.Errorf("expected 1 PackageTx call, got %d", pkg.callCount())
		}
		// Send() only guarantees a local write; the server may not have
		// processed the envelope yet by the time Submit returns.
		waitFor(t, 2*time.Second, func() bool { return fake.receivedCount() == 1 })
	})

	t.Run("packager error", func(t *testing.T) {
		addr, fake := startFakeOrderer(t)
		pkg := &mockPackager{err: errors.New("boom")}
		s, err := NewSubmitter(t.Context(), []OrdererConf{testOrdererConf(addr)}, pkg, 0, sdk.NoOpLogger{})
		if err != nil {
			t.Fatalf("NewSubmitter: %v", err)
		}
		t.Cleanup(func() { s.Close() }) //nolint:errcheck

		err = s.Submit(t.Context(), sdk.Endorsement{})
		if err == nil || !strings.Contains(err.Error(), "package proposal") {
			t.Fatalf("expected package proposal error, got %v", err)
		}
		if fake.receivedCount() != 0 {
			t.Errorf("orderer should not have been contacted, got %d envelopes", fake.receivedCount())
		}
	})

	t.Run("broadcast majority failure", func(t *testing.T) {
		addr := deadOrdererAddr(t)
		pkg := &mockPackager{}
		s, err := NewSubmitter(t.Context(), []OrdererConf{testOrdererConf(addr)}, pkg, 0, sdk.NoOpLogger{})
		if err != nil {
			t.Fatalf("NewSubmitter: %v", err)
		}
		t.Cleanup(func() { s.Close() }) //nolint:errcheck

		if err := s.Submit(t.Context(), sdk.Endorsement{}); err == nil {
			t.Fatal("expected broadcast error")
		}
	})
}

// --- broadcastEnv-equivalent (majority-quorum boundary) ---
//
// Submit inlines the quorum-broadcast loop rather than exposing it as a
// separate method; these tests exercise it through Submit with a bare
// Submitter literal so each case can mix healthy and dead orderers directly.

func TestSubmitter_QuorumBroadcast(t *testing.T) {
	newOrderer := func(t *testing.T, addr string) *Orderer {
		t.Helper()
		o, err := NewOrderer(t.Context(), testOrdererConf(addr))
		if err != nil {
			t.Fatalf("NewOrderer: %v", err)
		}
		t.Cleanup(func() { o.Close() }) //nolint:errcheck
		return o
	}

	t.Run("all succeed", func(t *testing.T) {
		okAddr, _ := startFakeOrderer(t)
		s := Submitter{orderers: []*Orderer{newOrderer(t, okAddr), newOrderer(t, okAddr)}, packager: &mockPackager{}, logger: sdk.NoOpLogger{}}
		if err := s.Submit(t.Context(), sdk.Endorsement{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("minority failure still succeeds", func(t *testing.T) {
		okAddr, _ := startFakeOrderer(t)
		failAddr := deadOrdererAddr(t)
		s := Submitter{
			orderers: []*Orderer{newOrderer(t, okAddr), newOrderer(t, okAddr), newOrderer(t, failAddr)},
			packager: &mockPackager{},
			logger:   sdk.NoOpLogger{},
		}
		if err := s.Submit(t.Context(), sdk.Endorsement{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("exact half failure still succeeds (tie goes to success)", func(t *testing.T) {
		okAddr, _ := startFakeOrderer(t)
		failAddr := deadOrdererAddr(t)
		s := Submitter{
			orderers: []*Orderer{newOrderer(t, okAddr), newOrderer(t, failAddr)},
			packager: &mockPackager{},
			logger:   sdk.NoOpLogger{},
		}
		if err := s.Submit(t.Context(), sdk.Endorsement{}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("majority failure errors", func(t *testing.T) {
		okAddr, _ := startFakeOrderer(t)
		failAddr := deadOrdererAddr(t)
		s := Submitter{
			orderers: []*Orderer{newOrderer(t, okAddr), newOrderer(t, failAddr), newOrderer(t, failAddr)},
			packager: &mockPackager{},
			logger:   sdk.NoOpLogger{},
		}
		if err := s.Submit(t.Context(), sdk.Endorsement{}); err == nil {
			t.Fatal("expected error")
		}
	})
}

// --- Close ---

func TestSubmitter_Close(t *testing.T) {
	addr1, _ := startFakeOrderer(t)
	addr2, _ := startFakeOrderer(t)
	s, err := NewSubmitter(t.Context(), []OrdererConf{testOrdererConf(addr1), testOrdererConf(addr2)}, &mockPackager{}, 0, sdk.NoOpLogger{})
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A closed submitter can no longer broadcast: every orderer's stream refuses
	// to (re)open, so Submit surfaces a majority-failure error.
	if err := s.Submit(t.Context(), sdk.Endorsement{}); err == nil {
		t.Fatal("expected error submitting after Close")
	}
}
