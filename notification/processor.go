/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
)

// NewProcessor creates a new notification processor with the given handlers.
func NewProcessor(handlers []TxStatusHandler, log sdk.Logger) *Processor {
	return &Processor{
		handlers: handlers,
		log:      log,
	}
}

// Processor manages transaction status event handlers.
type Processor struct {
	handlers []TxStatusHandler
	log      sdk.Logger
}

// ProcessStatuses executes all handlers for a batch of transaction status events.
// If any handler fails, ProcessStatuses returns an error and stops processing.
func (p *Processor) ProcessStatuses(ctx context.Context, events []TxStatusEvent) error {
	if len(events) == 0 {
		return nil
	}

	for _, h := range p.handlers {
		if err := h.Handle(ctx, events); err != nil {
			return fmt.Errorf("handle batch: %w", err)
		}
	}
	return nil
}

// NotificationPeer is the interface for opening a notification stream.
// It is satisfied by network/fabricx.Peer.
type NotificationPeer interface {
	Notify(ctx context.Context, txIDs <-chan []string, processor *Processor, timeout time.Duration) error
}

// Notifier manages subscriptions to the Fabric-X Notification Service.
// It delegates the gRPC stream to a NotificationPeer (typically a fabricx.Peer),
// pre-binding the processor and timeout for each Subscribe call.
type Notifier struct {
	peer           NotificationPeer
	processor      *Processor
	defaultTimeout time.Duration
}

// NewNotifier creates a new Notifier that subscribes for transaction status events
// via the given peer's notification stream.
func NewNotifier(peer NotificationPeer, processor *Processor) *Notifier {
	return &Notifier{
		peer:           peer,
		processor:      processor,
		defaultTimeout: 3 * time.Minute,
	}
}

// Subscribe opens a notification stream and subscribes to transaction IDs
// sent on the provided channel. Status updates are processed through the
// registered handlers. The function blocks until the context is canceled
// or an error occurs.
//
// Best practice: start Subscribe BEFORE submitting transactions to avoid
// missing notifications if transactions complete quickly.
func (n *Notifier) Subscribe(ctx context.Context, txIDs <-chan []string) error {
	return n.peer.Notify(ctx, txIDs, n.processor, n.defaultTimeout)
}

// SetDefaultTimeout changes the default timeout for future subscription requests.
func (n *Notifier) SetDefaultTimeout(timeout time.Duration) {
	n.defaultTimeout = timeout
}
