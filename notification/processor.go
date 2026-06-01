/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification

import (
	"context"
	"fmt"

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

// Made with Bob
