/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"github.com/hyperledger/fabric-x-sdk/notification"
)

// mockAllTxPeer captures the request and replays a fixed sequence of batches.
type mockAllTxPeer struct {
	req     *notification.StreamAllRequest
	batches []notification.AllTxBatch
	err     error
}

func (m *mockAllTxPeer) StreamAllTransactions(ctx context.Context, req *notification.StreamAllRequest, processor notification.AllTxProcessor) error {
	m.req = req
	for _, b := range m.batches {
		if err := processor.ProcessBatch(ctx, b); err != nil {
			return err
		}
	}
	return m.err
}

// mockAllTxHandler collects batches for assertion. Handlers run on the streamer's
// handler goroutine while the test reads from its own, so the slice is guarded.
type mockAllTxHandler struct {
	mu      sync.Mutex
	batches []notification.AllTxBatch
	err     error
}

func (m *mockAllTxHandler) HandleBatch(_ context.Context, batch notification.AllTxBatch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.batches = append(m.batches, batch)
	return m.err
}

func (m *mockAllTxHandler) seen() []notification.AllTxBatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]notification.AllTxBatch(nil), m.batches...)
}

func TestAllTxStreamer_DeliversBatchesToHandlers(t *testing.T) {
	batches := []notification.AllTxBatch{
		{BlockNumber: 1, Events: []notification.CommittedTxEvent{
			{Transaction: blocks.Transaction{ID: "tx1", Status: blocks.StatusCommitted}, BlockNum: 1},
		}},
		{BlockNumber: 2, Events: []notification.CommittedTxEvent{
			{Transaction: blocks.Transaction{ID: "tx2", Status: blocks.StatusMVCCConflict}, BlockNum: 2},
		}},
	}

	peer := &mockAllTxPeer{batches: batches}
	handler := &mockAllTxHandler{}
	streamer := notification.NewAllTxStreamer(peer, []notification.AllTxHandler{handler}, sdk.NewTestLogger(t, "test"))

	req := &notification.StreamAllRequest{}
	if err := streamer.Stream(context.Background(), req); err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	// The peer's loop returned before the handler goroutine necessarily drained the
	// queue; Stream is what guarantees both batches were processed by the time it
	// returns, buffered ones included.
	seen := handler.seen()
	if len(seen) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(seen))
	}
	if seen[0].BlockNumber != 1 || seen[0].Events[0].ID != "tx1" {
		t.Errorf("unexpected first batch: %+v", seen[0])
	}
	if seen[1].BlockNumber != 2 || seen[1].Events[0].ID != "tx2" {
		t.Errorf("unexpected second batch: %+v", seen[1])
	}
}

func TestAllTxStreamer_MultipleHandlers(t *testing.T) {
	batches := []notification.AllTxBatch{
		{BlockNumber: 5, Events: []notification.CommittedTxEvent{
			{Transaction: blocks.Transaction{ID: "txA", Status: blocks.StatusCommitted}, BlockNum: 5},
		}},
	}

	peer := &mockAllTxPeer{batches: batches}
	h1 := &mockAllTxHandler{}
	h2 := &mockAllTxHandler{}
	streamer := notification.NewAllTxStreamer(peer, []notification.AllTxHandler{h1, h2}, sdk.NewTestLogger(t, "test"))

	if err := streamer.Stream(context.Background(), &notification.StreamAllRequest{}); err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	if len(h1.seen()) != 1 || len(h2.seen()) != 1 {
		t.Fatalf("both handlers should receive the batch")
	}
}

func TestAllTxStreamer_HandlerErrorStopsProcessing(t *testing.T) {
	batches := []notification.AllTxBatch{
		{BlockNumber: 1},
		{BlockNumber: 2},
	}

	handlerErr := errors.New("handler failure")
	peer := &mockAllTxPeer{batches: batches}
	h := &mockAllTxHandler{err: handlerErr}
	streamer := notification.NewAllTxStreamer(peer, []notification.AllTxHandler{h}, sdk.NewTestLogger(t, "test"))

	err := streamer.Stream(context.Background(), &notification.StreamAllRequest{})
	if err == nil {
		t.Fatal("expected an error from handler failure")
	}
	if !errors.Is(err, handlerErr) {
		t.Errorf("expected handler error to be wrapped, got: %v", err)
	}
	// Only the first batch should have been handled: the chain stops at the first
	// error and does not go on to the batches already buffered behind it.
	if n := len(h.seen()); n != 1 {
		t.Errorf("expected 1 batch before error, got %d", n)
	}
}

func TestAllTxStreamer_RequestPassedToPeer(t *testing.T) {
	peer := &mockAllTxPeer{}
	streamer := notification.NewAllTxStreamer(peer, nil, sdk.NewTestLogger(t, "test"))

	req := &notification.StreamAllRequest{
		FilterNamespaces:     []string{"mycc"},
		FilterStatus:         []blocks.Status{blocks.StatusCommitted},
		IncludeReadWriteSets: true,
	}
	_ = streamer.Stream(context.Background(), req)

	if peer.req != req {
		t.Error("StreamAllRequest was not passed through to the peer")
	}
}

func TestAllTxStreamer_PeerErrorPropagates(t *testing.T) {
	peerErr := errors.New("stream broken")
	peer := &mockAllTxPeer{err: peerErr}
	streamer := notification.NewAllTxStreamer(peer, nil, sdk.NewTestLogger(t, "test"))

	err := streamer.Stream(context.Background(), &notification.StreamAllRequest{})
	if !errors.Is(err, peerErr) {
		t.Errorf("expected peer error to propagate, got: %v", err)
	}
}

// funcAllTxHandler adapts a plain function to notification.AllTxHandler.
type funcAllTxHandler func(context.Context, notification.AllTxBatch) error

func (f funcAllTxHandler) HandleBatch(ctx context.Context, b notification.AllTxBatch) error {
	return f(ctx, b)
}

// deliveringPeer feeds nBatches batches and reports each ProcessBatch call that
// returned, so a test can tell how far the receive loop ran ahead of the handlers.
// It then blocks until the context is cancelled, like a live stream with no traffic.
type deliveringPeer struct {
	nBatches int
	accepted chan uint64
}

func (p *deliveringPeer) StreamAllTransactions(ctx context.Context, _ *notification.StreamAllRequest, proc notification.AllTxProcessor) error {
	for i := range p.nBatches {
		if err := proc.ProcessBatch(ctx, notification.AllTxBatch{BlockNumber: uint64(i)}); err != nil {
			return err
		}
		p.accepted <- uint64(i)
	}
	<-ctx.Done()
	return nil
}

// TestAllTxStreamer_ReceiveLoopNotBlockedByHandler is the regression test for
// receiving and processing sharing a goroutine: a handler still applying block 0 must
// not stop the peer's receive loop from taking the blocks queued behind it. Before the
// hand-off existed this test could not get past the first batch.
func TestAllTxStreamer_ReceiveLoopNotBlockedByHandler(t *testing.T) {
	const nBatches = 8 // below AllTxQueueDepth, so backpressure is not in play here

	peer := &deliveringPeer{nBatches: nBatches, accepted: make(chan uint64, nBatches)}

	release := make(chan struct{})
	firstEntered := make(chan struct{})
	var once sync.Once
	handler := funcAllTxHandler(func(_ context.Context, b notification.AllTxBatch) error {
		if b.BlockNumber == 0 {
			once.Do(func() { close(firstEntered) })
			<-release // stuck applying block 0 for as long as the test likes
		}
		return nil
	})

	streamer := notification.NewAllTxStreamer(peer, []notification.AllTxHandler{handler}, sdk.NewTestLogger(t, "test"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- streamer.Stream(ctx, &notification.StreamAllRequest{}) }()

	<-firstEntered
	for i := range nBatches {
		select {
		case <-peer.accepted:
		case <-time.After(10 * time.Second):
			t.Fatalf("receive loop stalled behind the blocked handler after %d of %d batches", i, nBatches)
		}
	}

	close(release)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Stream returned %v, want nil on cancellation", err)
	}
}

// TestAllTxStreamer_DrainsQueueInOrderUnderBackpressure covers the other half of the
// hand-off: with more blocks in flight than the queue holds, the stream must slow to
// the handlers' pace rather than drop anything, and block order must survive. There is
// no historical replay behind this feed, so a lost batch is lost for good.
func TestAllTxStreamer_DrainsQueueInOrderUnderBackpressure(t *testing.T) {
	nBatches := notification.AllTxQueueDepth + 10

	batches := make([]notification.AllTxBatch, nBatches)
	for i := range batches {
		batches[i] = notification.AllTxBatch{BlockNumber: uint64(i)}
	}

	// Written only by the handler goroutine and read once Stream has returned, which
	// happens after that goroutine has exited.
	var order []uint64
	handler := funcAllTxHandler(func(_ context.Context, b notification.AllTxBatch) error {
		time.Sleep(100 * time.Microsecond) // let the receive loop run ahead and fill up
		order = append(order, b.BlockNumber)
		return nil
	})

	peer := &mockAllTxPeer{batches: batches}
	streamer := notification.NewAllTxStreamer(peer, []notification.AllTxHandler{handler}, sdk.NewTestLogger(t, "test"))

	if err := streamer.Stream(context.Background(), &notification.StreamAllRequest{}); err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	if len(order) != nBatches {
		t.Fatalf("expected all %d batches handled, got %d", nBatches, len(order))
	}
	for i, got := range order {
		if got != uint64(i) {
			t.Fatalf("batches handled out of order at index %d: got block %d", i, got)
		}
	}
}

func TestCommittedTxEvent_Valid(t *testing.T) {
	var committed notification.CommittedTxEvent
	committed.SetStatus(blocks.StatusCommitted, 0, "")
	if !committed.Valid() {
		t.Error("COMMITTED event should be valid")
	}

	var aborted notification.CommittedTxEvent
	aborted.SetStatus(blocks.StatusMVCCConflict, 0, "")
	if aborted.Valid() {
		t.Error("ABORTED event should not be valid")
	}
}
