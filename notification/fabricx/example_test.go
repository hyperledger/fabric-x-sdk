/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx_test

import (
	"context"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/notification"
	"github.com/hyperledger/fabric-x-sdk/notification/fabricx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// txStatusLogger is a simple handler that logs transaction status events
type txStatusLogger struct {
	log sdk.Logger
}

func (h *txStatusLogger) Handle(ctx context.Context, events []notification.TxStatusEvent) error {
	for _, event := range events {
		if event.Valid() {
			h.log.Infof("Transaction %s COMMITTED at block %d, tx %d",
				event.TxID, event.BlockNum, event.TxNum)
		} else {
			h.log.Warnf("Transaction %s FAILED with status %v at block %d, tx %d",
				event.TxID, event.Status, event.BlockNum, event.TxNum)
		}
	}
	return nil
}

// ExampleNotificationClient_Subscribe demonstrates basic usage of the notification client
func ExampleNotificationClient_Subscribe() {
	ctx := context.Background()
	log := sdk.NewStdLogger("notification-example")

	// Create processor with handlers
	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{
			&txStatusLogger{log: log},
		},
		log,
	)

	// Connect to Fabric-X Sidecar
	client, err := fabricx.NewNotificationClient(
		"localhost:7053", // Sidecar endpoint
		processor,
		log,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Errorf("Failed to create client: %v", err)
		return
	}
	defer client.Close()

	// Create channel for sending transaction IDs to monitor
	txIDs := make(chan []string, 10)

	// IMPORTANT: Start Subscribe BEFORE submitting transactions to avoid missing notifications
	go func() {
		if err := client.Subscribe(ctx, txIDs); err != nil {
			log.Errorf("Subscribe error: %v", err)
		}
	}()

	// Send transaction IDs to monitor (typically obtained after endorsement)
	txIDs <- []string{"tx-abc123", "tx-def456", "tx-ghi789"}

	// Now submit your transactions to the ordering service...
	// (submission code not shown)

	// Later, you can send more transaction IDs to monitor
	time.Sleep(1 * time.Second)
	txIDs <- []string{"tx-jkl012", "tx-mno345"}

	// Wait for notifications or timeout
	time.Sleep(5 * time.Minute)
}

// ExampleNotificationClient_simpleUsage demonstrates simple one-time subscription
func ExampleNotificationClient_simpleUsage() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log := sdk.NewStdLogger("notification-example")

	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{
			&txStatusLogger{log: log},
		},
		log,
	)

	client, err := fabricx.NewNotificationClient(
		"localhost:7053",
		processor,
		log,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Errorf("Failed to create client: %v", err)
		return
	}
	defer client.Close()

	// For simple one-time subscription, create channel, send once, and close
	txIDs := make(chan []string, 1)
	txIDs <- []string{"tx1", "tx2", "tx3"}
	close(txIDs) // Close channel after sending

	// Subscribe will process these and exit when channel is closed
	if err := client.Subscribe(ctx, txIDs); err != nil {
		log.Errorf("Subscribe error: %v", err)
	}
}

// ExampleNotificationClient_customTimeout demonstrates using custom timeouts
func ExampleNotificationClient_customTimeout() {
	ctx := context.Background()
	log := sdk.NewStdLogger("notification-example")

	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{
			&txStatusLogger{log: log},
		},
		log,
	)

	client, err := fabricx.NewNotificationClient(
		"localhost:7053",
		processor,
		log,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Errorf("Failed to create client: %v", err)
		return
	}
	defer client.Close()

	// Set a custom default timeout for all subscriptions
	client.SetDefaultTimeout(30 * time.Second)

	txIDs := make(chan []string, 1)
	txIDs <- []string{"fast-tx-1", "fast-tx-2"}
	close(txIDs)

	if err := client.Subscribe(ctx, txIDs); err != nil {
		log.Errorf("Subscribe error: %v", err)
	}
}

// Made with Bob
