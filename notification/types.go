/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package notification provides tools to track transaction status using the
// Fabric-X Notification Service. Unlike the block-based processor which receives
// all transactions passively, this system actively subscribes to specific
// transaction IDs and receives status updates.
package notification

import (
	"context"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
)

// TxStatusEvent represents a transaction status notification from the
// Fabric-X Notification Service.
type TxStatusEvent struct {
	TxID     string
	BlockNum uint64
	TxNum    uint32
	Status   committerpb.Status
}

// Valid returns true if the transaction was committed successfully.
func (e TxStatusEvent) Valid() bool {
	return e.Status == committerpb.Status_COMMITTED
}

// TxStatusHandler processes batches of transaction status events.
// Handlers are invoked sequentially for each batch of events received
// from the notification service.
type TxStatusHandler interface {
	Handle(ctx context.Context, events []TxStatusEvent) error
}
