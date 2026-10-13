/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	ordererpb "github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	sdk "github.com/hyperledger/fabric-x-sdk"
)

// recordingLogger keeps the warnings it is given.
type recordingLogger struct {
	sdk.NoOpLogger

	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) Warnf(template string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(template, args...))
}

func (l *recordingLogger) warnings() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.warns...)
}

// TestOrderer_LogsRejectedBroadcast: the router rejects a request (for example one whose
// signature does not satisfy the Writers policy) with a reply on the stream, while the
// Broadcast call itself has long returned nil. The reply must not be lost.
func TestOrderer_LogsRejectedBroadcast(t *testing.T) {
	const info = "request structure verification error: signature did not satisfy policy /Channel/Writers"
	addr, fake := startFakeOrdererReplying(t, &ordererpb.BroadcastResponse{Status: common.Status_INTERNAL_SERVER_ERROR, Info: info})

	log := &recordingLogger{}
	s, err := NewSubmitter(t.Context(), []OrdererConf{testOrdererConf(addr)}, &mockPackager{}, 0, log)
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck,gosec // Best-effort test cleanup.

	// still nil: the reply is asynchronous, so Submit cannot report it
	if err := s.Submit(t.Context(), sdk.Endorsement{}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return len(log.warnings()) > 0 })

	got := log.warnings()[0]
	for _, want := range []string{addr, "INTERNAL_SERVER_ERROR", info} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q does not mention %q", got, want)
		}
	}
	if fake.receivedCount() != 1 {
		t.Errorf("expected 1 envelope at the orderer, got %d", fake.receivedCount())
	}
}

// TestOrderer_AcceptedBroadcastIsQuiet: SUCCESS replies are the normal case and must not
// produce log noise.
func TestOrderer_AcceptedBroadcastIsQuiet(t *testing.T) {
	addr, fake := startFakeOrderer(t)

	log := &recordingLogger{}
	s, err := NewSubmitter(t.Context(), []OrdererConf{testOrdererConf(addr)}, &mockPackager{}, 0, log)
	if err != nil {
		t.Fatalf("NewSubmitter: %v", err)
	}
	t.Cleanup(func() { s.Close() }) //nolint:errcheck,gosec // Best-effort test cleanup.

	for range 3 {
		if err := s.Submit(t.Context(), sdk.Endorsement{}); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	// the fake replies right after receiving, so once it has seen everything the replies are on their way
	waitFor(t, 5*time.Second, func() bool { return fake.receivedCount() == 3 })
	time.Sleep(100 * time.Millisecond)

	if w := log.warnings(); len(w) != 0 {
		t.Errorf("expected no warnings, got %q", w)
	}
}

// TestNewOrderer_NilLogger: a nil logger discards rejections instead of panicking in the
// goroutine that reads the replies.
func TestNewOrderer_NilLogger(t *testing.T) {
	addr, fake := startFakeOrdererReplying(t, &ordererpb.BroadcastResponse{Status: common.Status_SERVICE_UNAVAILABLE, Info: "throttled"})

	o, err := NewOrderer(t.Context(), testOrdererConf(addr), nil)
	if err != nil {
		t.Fatalf("NewOrderer: %v", err)
	}
	t.Cleanup(func() { o.Close() }) //nolint:errcheck,gosec // Best-effort test cleanup.

	if err := o.Broadcast(t.Context(), &common.Envelope{Payload: []byte("p")}); err != nil {
		t.Fatalf("Broadcast: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return fake.receivedCount() == 1 })
	time.Sleep(100 * time.Millisecond) // a panic in the reader goroutine would have taken the test binary down by now
}
