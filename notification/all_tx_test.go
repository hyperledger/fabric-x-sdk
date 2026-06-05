/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification_test

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/hyperledger/fabric-x-sdk"
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

// mockAllTxHandler collects batches for assertion.
type mockAllTxHandler struct {
	batches []notification.AllTxBatch
	err     error
}

func (m *mockAllTxHandler) HandleBatch(_ context.Context, batch notification.AllTxBatch) error {
	m.batches = append(m.batches, batch)
	return m.err
}

func TestAllTxStreamer_DeliversBatchesToHandlers(t *testing.T) {
	batches := []notification.AllTxBatch{
		{BlockNumber: 1, Events: []notification.CommittedTxEvent{
			{TxID: "tx1", BlockNum: 1, TxNum: 0, Status: notification.StatusCommitted},
		}},
		{BlockNumber: 2, Events: []notification.CommittedTxEvent{
			{TxID: "tx2", BlockNum: 2, TxNum: 0, Status: notification.StatusInvalid},
		}},
	}

	peer := &mockAllTxPeer{batches: batches}
	handler := &mockAllTxHandler{}
	streamer := notification.NewAllTxStreamer(peer, []notification.AllTxHandler{handler}, sdk.NewTestLogger(t, "test"))

	req := &notification.StreamAllRequest{}
	if err := streamer.Stream(context.Background(), req); err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	if len(handler.batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(handler.batches))
	}
	if handler.batches[0].BlockNumber != 1 || handler.batches[0].Events[0].TxID != "tx1" {
		t.Errorf("unexpected first batch: %+v", handler.batches[0])
	}
	if handler.batches[1].BlockNumber != 2 || handler.batches[1].Events[0].TxID != "tx2" {
		t.Errorf("unexpected second batch: %+v", handler.batches[1])
	}
}

func TestAllTxStreamer_MultipleHandlers(t *testing.T) {
	batches := []notification.AllTxBatch{
		{BlockNumber: 5, Events: []notification.CommittedTxEvent{
			{TxID: "txA", BlockNum: 5, TxNum: 0, Status: notification.StatusCommitted},
		}},
	}

	peer := &mockAllTxPeer{batches: batches}
	h1 := &mockAllTxHandler{}
	h2 := &mockAllTxHandler{}
	streamer := notification.NewAllTxStreamer(peer, []notification.AllTxHandler{h1, h2}, sdk.NewTestLogger(t, "test"))

	if err := streamer.Stream(context.Background(), &notification.StreamAllRequest{}); err != nil {
		t.Fatalf("Stream failed: %v", err)
	}

	if len(h1.batches) != 1 || len(h2.batches) != 1 {
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
	// Only the first batch should have been seen; the error stops the peer loop.
	if len(h.batches) != 1 {
		t.Errorf("expected 1 batch before error, got %d", len(h.batches))
	}
}

func TestAllTxStreamer_RequestPassedToPeer(t *testing.T) {
	peer := &mockAllTxPeer{}
	streamer := notification.NewAllTxStreamer(peer, nil, sdk.NewTestLogger(t, "test"))

	req := &notification.StreamAllRequest{
		FilterNamespaces:     []string{"mycc"},
		FilterStatus:         []notification.Status{notification.StatusCommitted},
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

func TestCommittedTxEvent_Valid(t *testing.T) {
	committed := notification.CommittedTxEvent{Status: notification.StatusCommitted}
	if !committed.Valid() {
		t.Error("COMMITTED event should be valid")
	}

	aborted := notification.CommittedTxEvent{Status: notification.StatusInvalid}
	if aborted.Valid() {
		t.Error("ABORTED event should not be valid")
	}
}
