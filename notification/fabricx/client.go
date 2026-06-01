/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/notification"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/durationpb"
)

// NotificationClient manages subscriptions to the Fabric-X Notification Service.
// It opens a bidirectional gRPC stream to the Sidecar's Notifier service,
// sends subscription requests for specific transaction IDs, and processes
// status updates through registered handlers.
type NotificationClient struct {
	conn      *grpc.ClientConn
	client    committerpb.NotifierClient
	processor *notification.Processor
	log       sdk.Logger

	// Configuration
	defaultTimeout time.Duration
}

// NewNotificationClient creates a new notification client connected to the
// Fabric-X Sidecar's Notifier service.
func NewNotificationClient(
	endpoint string,
	processor *notification.Processor,
	log sdk.Logger,
	opts ...grpc.DialOption,
) (*NotificationClient, error) {
	conn, err := grpc.NewClient(endpoint, opts...)
	if err != nil {
		return nil, fmt.Errorf("dial sidecar: %w", err)
	}

	return &NotificationClient{
		conn:           conn,
		client:         committerpb.NewNotifierClient(conn),
		processor:      processor,
		log:            log,
		defaultTimeout: 3 * time.Minute,
	}, nil
}

// Subscribe opens a notification stream and subscribes to transaction IDs
// sent on the provided channel. Status updates are automatically processed
// through the registered handlers. The function blocks until the context
// is canceled or an error occurs.
//
// Usage:
//
//	txIDs := make(chan []string, 10)
//	go client.Subscribe(ctx, txIDs)
//
//	// Send transaction IDs to monitor
//	txIDs <- []string{"tx1", "tx2"}
//	// Later, send more
//	txIDs <- []string{"tx3", "tx4"}
//
// Best practice: Start Subscribe BEFORE submitting transactions to ensure you
// don't miss notifications if transactions complete quickly.
func (c *NotificationClient) Subscribe(ctx context.Context, txIDs <-chan []string) error {
	stream, err := c.client.OpenNotificationStream(ctx)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	// Create a cancellable context for coordinating goroutines
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, 1)

	// Start receive loop in background
	go func() {
		err := c.receiveLoop(ctx, stream)
		if err != nil {
			select {
			case errCh <- err:
				cancel() // Cancel to stop other goroutines
			default:
			}
		}
	}()

	// Start send loop in background
	go func() {
		err := c.sendLoop(ctx, stream, txIDs)
		if err != nil {
			select {
			case errCh <- err:
				cancel() // Cancel to stop other goroutines
			default:
			}
		}
	}()

	// Wait for first error or context cancellation
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return nil
	}
}

func (c *NotificationClient) sendLoop(
	ctx context.Context,
	stream committerpb.Notifier_OpenNotificationStreamClient,
	txIDs <-chan []string,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case batch, ok := <-txIDs:
			if !ok {
				// Channel closed, stop sending
				return nil
			}
			if len(batch) == 0 {
				continue
			}

			req := &committerpb.NotificationRequest{
				TxStatusRequest: &committerpb.TxIDsBatch{
					TxIds: batch,
				},
				Timeout: durationpb.New(c.defaultTimeout),
			}

			if err := stream.Send(req); err != nil {
				return fmt.Errorf("send request: %w", err)
			}

			c.log.Debugf("subscribed to %d transaction IDs with timeout %v", len(batch), c.defaultTimeout)
		}
	}
}

func (c *NotificationClient) receiveLoop(
	ctx context.Context,
	stream committerpb.Notifier_OpenNotificationStreamClient,
) error {
	// Create channel for decoupling recv from processing
	// Buffer size allows recv to continue while processing is ongoing
	eventsCh := make(chan []notification.TxStatusEvent, 10)
	errCh := make(chan error, 1)

	// Start processing goroutine
	go func() {
		errCh <- c.processLoop(ctx, eventsCh)
	}()

	// Receive loop - should not block on processing
	for {
		res, err := stream.Recv()
		if err != nil {
			close(eventsCh) // Signal processor to stop
			if err == io.EOF || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("recv: %w", err)
		}

		events := c.convertResponse(res)

		// Send to processing goroutine without blocking recv
		if len(events) > 0 {
			select {
			case eventsCh <- events:
				// Sent successfully
			case <-ctx.Done():
				close(eventsCh)
				return nil
			case err := <-errCh:
				// Processor failed
				close(eventsCh)
				return err
			}
		}
	}
}

// processLoop runs in a separate goroutine to process events without blocking recv
func (c *NotificationClient) processLoop(
	ctx context.Context,
	eventsCh <-chan []notification.TxStatusEvent,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case events, ok := <-eventsCh:
			if !ok {
				// Channel closed, stop processing
				return nil
			}
			if err := c.processor.ProcessStatuses(ctx, events); err != nil {
				c.log.Warnf("process status batch: %v", err)
				// Continue processing despite handler errors
			}
		}
	}
}

func (c *NotificationClient) convertResponse(res *committerpb.NotificationResponse) []notification.TxStatusEvent {
	var events []notification.TxStatusEvent

	// Convert status events
	for _, txStatus := range res.TxStatusEvents {
		events = append(events, notification.TxStatusEvent{
			TxID:     txStatus.Ref.TxId,
			BlockNum: txStatus.Ref.BlockNum,
			TxNum:    txStatus.Ref.TxNum,
			Status:   txStatus.Status,
		})
	}

	// Handle timeouts - create pseudo-events
	// Note: Timeouts don't have a specific status code, so we use a zero-value Status
	if len(res.TimeoutTxIds) > 0 {
		c.log.Warnf("timeout for %d transactions: %v", len(res.TimeoutTxIds), res.TimeoutTxIds)
		for _, txID := range res.TimeoutTxIds {
			events = append(events, notification.TxStatusEvent{
				TxID:   txID,
				Status: 0, // Zero value indicates timeout
			})
		}
	}

	// Note: RejectedTxIds field was added in a later version of fabric-x-common.
	// The current version (v0.1.1-0.20260219094834-26c5a49ed548) only has
	// TxStatusEvents and TimeoutTxIds fields. Rejections will be handled
	// when the SDK upgrades to a newer version of fabric-x-common.

	return events
}

// SetDefaultTimeout changes the default timeout for future Subscribe calls.
func (c *NotificationClient) SetDefaultTimeout(timeout time.Duration) {
	c.defaultTimeout = timeout
}

// Close closes the connection to the Sidecar.
func (c *NotificationClient) Close() error {
	return c.conn.Close()
}

// Made with Bob
