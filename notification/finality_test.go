/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/notification"
)

// controlledPeer lets tests drive what peer.Notify returns.
// It exposes a sendEvent helper to deliver events to the running stream.
type controlledPeer struct {
	mu       sync.Mutex
	proc     *notification.Processor
	txIDsChs []chan []string // one per Notify call, in order

	// failFirst causes the first Notify call to return an error immediately.
	failFirst bool
	calls     int

	// statuses, when set, is served by GetTransactionStatus so tests can model a
	// transaction that reached finality while the stream was down.
	statuses map[string]notification.TxStatusEvent
}

// GetTransactionStatus implements notification.StatusQuerier.
func (p *controlledPeer) GetTransactionStatus(_ context.Context, txIDs []string) ([]notification.TxStatusEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []notification.TxStatusEvent
	for _, id := range txIDs {
		if e, ok := p.statuses[id]; ok {
			out = append(out, e)
		}
	}
	return out, nil
}

func (p *controlledPeer) Notify(ctx context.Context, txIDs <-chan []string, proc *notification.Processor, _ time.Duration) error {
	p.mu.Lock()
	p.proc = proc
	p.calls++
	fail := p.failFirst && p.calls == 1
	p.mu.Unlock()

	if fail {
		return errors.New("simulated stream failure")
	}

	// drain txIDs (so senders don't block) and wait for ctx
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-txIDs:
				if !ok {
					return
				}
			}
		}
	}()

	<-ctx.Done()
	return nil
}

// sendEvent delivers a status event to the currently active processor.
func (p *controlledPeer) sendEvent(t *testing.T, ctx context.Context, event notification.TxStatusEvent) {
	t.Helper()
	p.mu.Lock()
	proc := p.proc
	p.mu.Unlock()
	if proc == nil {
		t.Fatal("sendEvent: no active processor")
	}
	if err := proc.ProcessStatuses(ctx, []notification.TxStatusEvent{event}); err != nil {
		t.Fatalf("sendEvent ProcessStatuses: %v", err)
	}
}

func startListener(t *testing.T, peer *controlledPeer) (*notification.FinalityListener, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	l := notification.NewFinalityListener(peer, 0, sdk.NewTestLogger(t, "test"))
	done := make(chan struct{})
	go func() {
		l.Start(ctx)
		close(done)
	}()
	// Give Start a moment to open the stream.
	time.Sleep(20 * time.Millisecond)
	// The returned stop cancels Start and waits for it to exit, so it cannot log
	// through the test logger after the test has completed.
	stop := func() {
		cancel()
		<-done
	}
	return l, stop
}

// TestFinalityListener_ReceivesCommittedEvent verifies the happy path.
func TestFinalityListener_ReceivesCommittedEvent(t *testing.T) {
	peer := &controlledPeer{}
	l, cancel := startListener(t, peer)
	defer cancel()

	ctx := context.Background()
	ch, err := l.Register(ctx, "tx1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	peer.sendEvent(t, ctx, notification.TxStatusEvent{
		TxID:   "tx1",
		Status: notification.StatusCommitted,
	})

	select {
	case event := <-ch:
		if event.TxID != "tx1" || !event.Valid() {
			t.Errorf("unexpected event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for finality event")
	}
}

// TestFinalityListener_MultipleWaiters verifies concurrent registrations.
func TestFinalityListener_MultipleWaiters(t *testing.T) {
	peer := &controlledPeer{}
	l, cancel := startListener(t, peer)
	defer cancel()

	ctx := context.Background()
	ch1, err := l.Register(ctx, "txA")
	if err != nil {
		t.Fatalf("Register txA: %v", err)
	}
	ch2, err := l.Register(ctx, "txB")
	if err != nil {
		t.Fatalf("Register txB: %v", err)
	}

	peer.sendEvent(t, ctx, notification.TxStatusEvent{TxID: "txB", Status: notification.StatusCommitted})
	peer.sendEvent(t, ctx, notification.TxStatusEvent{TxID: "txA", Status: notification.StatusInvalid})

	for _, tc := range []struct {
		ch    <-chan notification.TxStatusEvent
		txID  string
		valid bool
	}{
		{ch1, "txA", false},
		{ch2, "txB", true},
	} {
		select {
		case event := <-tc.ch:
			if event.TxID != tc.txID {
				t.Errorf("expected txID %s, got %s", tc.txID, event.TxID)
			}
			if event.Valid() != tc.valid {
				t.Errorf("txID %s: expected valid=%v", tc.txID, tc.valid)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", tc.txID)
		}
	}
}

// TestFinalityListener_ContextCancellation verifies that WaitForFinality returns
// when the context expires.
func TestFinalityListener_ContextCancellation(t *testing.T) {
	peer := &controlledPeer{}
	l, cancel := startListener(t, peer)
	defer cancel()

	ctx, ctxCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer ctxCancel()

	_, err := l.WaitForFinality(ctx, "tx-never-committed")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
}

// TestFinalityListener_Unregister verifies that Unregister removes the entry so
// no stale channel blocks a reconnect re-subscription.
func TestFinalityListener_Unregister(t *testing.T) {
	peer := &controlledPeer{}
	l, cancel := startListener(t, peer)
	defer cancel()

	if _, err := l.Register(t.Context(), "tx-cancel"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	l.Unregister("tx-cancel") // should be a no-op thereafter

	// Sending an event for the unregistered txID must not panic or block.
	peer.sendEvent(t, context.Background(), notification.TxStatusEvent{
		TxID:   "tx-cancel",
		Status: notification.StatusCommitted,
	})
}

// TestFinalityListener_DuplicateRegister verifies that registering the same txID
// twice returns ErrAlreadyRegistered rather than orphaning the first waiter.
func TestFinalityListener_DuplicateRegister(t *testing.T) {
	peer := &controlledPeer{}
	l, cancel := startListener(t, peer)
	defer cancel()

	if _, err := l.Register(t.Context(), "dup"); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	if _, err := l.Register(t.Context(), "dup"); !errors.Is(err, notification.ErrAlreadyRegistered) {
		t.Errorf("expected ErrAlreadyRegistered, got: %v", err)
	}
}

// TestFinalityListener_RegisterBackpressure verifies that Register blocks rather
// than dropping a subscription when the internal channel is full, and then honors
// context cancellation. With no Start loop running, nothing drains the channel.
func TestFinalityListener_RegisterBackpressure(t *testing.T) {
	peer := &controlledPeer{}
	l := notification.NewFinalityListener(peer, 0, sdk.NewTestLogger(t, "test"))
	// Deliberately do NOT start l, so nothing drains the subscription channel.

	// Fill the buffered subscription channel (capacity matches the listener's
	// internal buffer of 256).
	for i := 0; i < 256; i++ {
		if _, err := l.Register(context.Background(), fmt.Sprintf("fill-%d", i)); err != nil {
			t.Fatalf("Register %d: %v", i, err)
		}
	}

	// The next Register has nowhere to enqueue its subscription; it must block
	// until ctx expires and return that error, never silently drop.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := l.Register(ctx, "overflow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded from a full subscription queue, got: %v", err)
	}
}

// TestFinalityListener_ReconnectResubscribes verifies that after a stream error,
// Start reconnects and re-subscribes pending txIDs.
func TestFinalityListener_ReconnectResubscribes(t *testing.T) {
	peer := &controlledPeer{failFirst: true}

	ctx, cancel := context.WithCancel(context.Background())
	l := notification.NewFinalityListener(peer, 0, sdk.NewTestLogger(t, "test"))
	done := make(chan struct{})
	go func() {
		l.Start(ctx)
		close(done)
	}()
	// Stop Start and wait for it to exit so it cannot log after the test ends.
	defer func() { cancel(); <-done }()

	// Wait for the first (failing) call to complete and the second to start.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		peer.mu.Lock()
		calls := peer.calls
		hasProc := peer.proc != nil
		peer.mu.Unlock()
		if calls >= 2 && hasProc {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	peer.mu.Lock()
	calls := peer.calls
	peer.mu.Unlock()
	if calls < 2 {
		t.Fatalf("expected at least 2 Notify calls (reconnect), got %d", calls)
	}

	// The listener should work normally on the second stream.
	ch, err := l.Register(ctx, "tx-after-reconnect")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	peer.sendEvent(t, context.Background(), notification.TxStatusEvent{
		TxID:   "tx-after-reconnect",
		Status: notification.StatusCommitted,
	})

	select {
	case event := <-ch:
		if !event.Valid() {
			t.Errorf("expected committed, got %v", event.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event after reconnect")
	}
}

// TestFinalityListener_ReconcilesOnReconnect verifies that a transaction which
// reaches finality while the stream is down is delivered via status
// reconciliation after reconnect, without a stream event.
func TestFinalityListener_ReconcilesOnReconnect(t *testing.T) {
	peer := &controlledPeer{
		failFirst: true,
		statuses: map[string]notification.TxStatusEvent{
			"tx-offline": {TxID: "tx-offline", Status: notification.StatusCommitted},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())

	l := notification.NewFinalityListener(peer, 0, sdk.NewTestLogger(t, "test"))
	ch, err := l.Register(ctx, "tx-offline")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	done := make(chan struct{})
	go func() {
		l.Start(ctx)
		close(done)
	}()
	// Stop Start and wait for it to exit so it cannot log after the test ends.
	defer func() { cancel(); <-done }()

	select {
	case event := <-ch:
		if !event.Valid() {
			t.Errorf("expected committed via reconciliation, got %v", event.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reconciled finality event")
	}
}

// TestErrTxRejected verifies the error type.
func TestErrTxRejected(t *testing.T) {
	err := &notification.ErrTxRejected{TxID: "abc", Status: notification.StatusInvalid}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}

	var target *notification.ErrTxRejected
	if !errors.As(err, &target) {
		t.Error("errors.As should match *ErrTxRejected")
	}
	if target.TxID != "abc" {
		t.Errorf("expected TxID abc, got %s", target.TxID)
	}
}
