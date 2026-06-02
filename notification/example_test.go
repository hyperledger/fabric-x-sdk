/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification_test

import (
	"context"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/network"
	fxnet "github.com/hyperledger/fabric-x-sdk/network/fabricx"
	"github.com/hyperledger/fabric-x-sdk/notification"
)

// txStatusLogger is a simple handler that logs transaction status events.
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

// ExampleNotifier_Subscribe demonstrates basic usage of the Notifier.
func ExampleNotifier_Subscribe() {
	ctx := context.Background()
	log := sdk.NewStdLogger("notification-example")

	peer, err := fxnet.NewPeer(
		network.PeerConf{
			Address: "localhost:7053",
			TLS:     network.TLSConfig{Mode: network.TLSModeNone},
		},
		"mychannel", nil,
	)
	if err != nil {
		log.Errorf("Failed to create peer: %v", err)
		return
	}
	defer peer.Close() //nolint:errcheck

	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{&txStatusLogger{log: log}},
		log,
	)

	notifier := notification.NewNotifier(peer, processor)

	// Create channel for sending transaction IDs to monitor.
	txIDs := make(chan []string, 10)

	// IMPORTANT: start Subscribe BEFORE submitting transactions to avoid missing notifications.
	go func() {
		if err := notifier.Subscribe(ctx, txIDs); err != nil {
			log.Errorf("Subscribe error: %v", err)
		}
	}()

	// Send transaction IDs to monitor (typically obtained after endorsement).
	txIDs <- []string{"tx-abc123", "tx-def456", "tx-ghi789"}

	// Later, send more transaction IDs.
	time.Sleep(1 * time.Second)
	txIDs <- []string{"tx-jkl012", "tx-mno345"}

	time.Sleep(5 * time.Minute)
}

// ExampleNotifier_Subscribe_simpleUsage demonstrates simple one-time subscription.
func ExampleNotifier_Subscribe_simpleUsage() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log := sdk.NewStdLogger("notification-example")

	peer, err := fxnet.NewPeer(
		network.PeerConf{
			Address: "localhost:7053",
			TLS:     network.TLSConfig{Mode: network.TLSModeNone},
		},
		"mychannel", nil,
	)
	if err != nil {
		log.Errorf("Failed to create peer: %v", err)
		return
	}
	defer peer.Close() //nolint:errcheck

	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{&txStatusLogger{log: log}},
		log,
	)

	notifier := notification.NewNotifier(peer, processor)

	// For a simple one-time subscription: send once, close the channel.
	txIDs := make(chan []string, 1)
	txIDs <- []string{"tx1", "tx2", "tx3"}
	close(txIDs)

	if err := notifier.Subscribe(ctx, txIDs); err != nil {
		log.Errorf("Subscribe error: %v", err)
	}
}

// ExampleNotifier_SetDefaultTimeout demonstrates using a custom timeout.
func ExampleNotifier_SetDefaultTimeout() {
	ctx := context.Background()
	log := sdk.NewStdLogger("notification-example")

	peer, err := fxnet.NewPeer(
		network.PeerConf{
			Address: "localhost:7053",
			TLS:     network.TLSConfig{Mode: network.TLSModeNone},
		},
		"mychannel", nil,
	)
	if err != nil {
		log.Errorf("Failed to create peer: %v", err)
		return
	}
	defer peer.Close() //nolint:errcheck

	processor := notification.NewProcessor(
		[]notification.TxStatusHandler{&txStatusLogger{log: log}},
		log,
	)

	notifier := notification.NewNotifier(peer, processor)
	notifier.SetDefaultTimeout(30 * time.Second)

	txIDs := make(chan []string, 1)
	txIDs <- []string{"fast-tx-1", "fast-tx-2"}
	close(txIDs)

	if err := notifier.Subscribe(ctx, txIDs); err != nil {
		log.Errorf("Subscribe error: %v", err)
	}
}
