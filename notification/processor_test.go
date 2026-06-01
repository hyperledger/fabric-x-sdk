/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification_test

import (
	"context"
	"testing"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/notification"
)

// mockHandler collects events for testing
type mockHandler struct {
	events [][]notification.TxStatusEvent
}

func (m *mockHandler) Handle(ctx context.Context, events []notification.TxStatusEvent) error {
	m.events = append(m.events, events)
	return nil
}

func TestProcessor(t *testing.T) {
	handler := &mockHandler{}
	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{handler},
		sdk.NewTestLogger(t, "test"),
	)

	// Test processing a batch of events
	events := []notification.TxStatusEvent{
		{
			TxID:     "tx1",
			BlockNum: 1,
			TxNum:    0,
			Status:   committerpb.Status_COMMITTED,
		},
		{
			TxID:     "tx2",
			BlockNum: 1,
			TxNum:    1,
			Status:   committerpb.Status_ABORTED_MVCC_CONFLICT,
		},
	}

	err := processor.ProcessStatuses(context.Background(), events)
	if err != nil {
		t.Fatalf("ProcessStatuses failed: %v", err)
	}

	if len(handler.events) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(handler.events))
	}

	if len(handler.events[0]) != 2 {
		t.Fatalf("expected 2 events in batch, got %d", len(handler.events[0]))
	}

	// Verify first event
	if handler.events[0][0].TxID != "tx1" {
		t.Errorf("expected tx1, got %s", handler.events[0][0].TxID)
	}
	if !handler.events[0][0].Valid() {
		t.Error("expected tx1 to be valid")
	}

	// Verify second event
	if handler.events[0][1].TxID != "tx2" {
		t.Errorf("expected tx2, got %s", handler.events[0][1].TxID)
	}
	if handler.events[0][1].Valid() {
		t.Error("expected tx2 to be invalid")
	}
}

func TestProcessorEmptyBatch(t *testing.T) {
	handler := &mockHandler{}
	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{handler},
		sdk.NewTestLogger(t, "test"),
	)

	// Test processing empty batch
	err := processor.ProcessStatuses(context.Background(), nil)
	if err != nil {
		t.Fatalf("ProcessStatuses failed: %v", err)
	}

	if len(handler.events) != 0 {
		t.Fatalf("expected 0 batches, got %d", len(handler.events))
	}
}

func TestProcessorMultipleHandlers(t *testing.T) {
	handler1 := &mockHandler{}
	handler2 := &mockHandler{}
	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{handler1, handler2},
		sdk.NewTestLogger(t, "test"),
	)

	events := []notification.TxStatusEvent{
		{
			TxID:     "tx1",
			BlockNum: 1,
			TxNum:    0,
			Status:   committerpb.Status_COMMITTED,
		},
	}

	err := processor.ProcessStatuses(context.Background(), events)
	if err != nil {
		t.Fatalf("ProcessStatuses failed: %v", err)
	}

	// Both handlers should receive the same batch
	if len(handler1.events) != 1 || len(handler2.events) != 1 {
		t.Fatalf("expected both handlers to receive 1 batch")
	}

	if len(handler1.events[0]) != 1 || len(handler2.events[0]) != 1 {
		t.Fatalf("expected both handlers to receive 1 event")
	}
}

// Made with Bob
