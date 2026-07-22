/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification

import (
	"context"
	"fmt"

	"github.com/hyperledger/fabric-x-common/api/applicationpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
)

// CommittedTxEvent is a single transaction event received from StreamAllTransactions.
// Namespaces, Endorsements, and Metadata are populated only when the corresponding flags were
// set in the StreamAllRequest.
type CommittedTxEvent struct {
	TxID         string
	BlockNum     uint64
	TxNum        uint32
	Status       Status
	Reason       string
	Namespaces   []*applicationpb.TxNamespace
	Endorsements []*applicationpb.Endorsements
	Metadata     [][]byte
}

// Valid returns true if the transaction was committed successfully.
func (e CommittedTxEvent) Valid() bool {
	return e.Status == StatusCommitted
}

// AllTxBatch is a batch of transaction events from a single committed block.
// Preserving block boundaries allows handlers to process a whole block atomically.
type AllTxBatch struct {
	BlockNumber uint64
	Events      []CommittedTxEvent
}

// AllTxHandler processes batches of committed transaction events.
// Handlers are invoked sequentially for each block's worth of events.
//
// HandleBatch runs on the stream's single receive goroutine and must not block:
// slow work stalls the loop and delays the entire committed-transaction feed.
// Offload anything slow to your own goroutine or queue.
type AllTxHandler interface {
	HandleBatch(ctx context.Context, batch AllTxBatch) error
}

// AllTxProcessor is the internal callback interface used by fabricx.Peer.StreamAllTransactions.
// It is satisfied by AllTxStreamer, which adapts it to the registered AllTxHandler chain.
type AllTxProcessor interface {
	ProcessBatch(ctx context.Context, batch AllTxBatch) error
}

// StreamAllRequest configures a StreamAllTransactions subscription.
// FilterNamespaces and FilterStatus narrow the event stream using OR logic within
// each filter and AND logic between them. Leaving a filter nil delivers all events.
type StreamAllRequest struct {
	// FilterNamespaces limits events to transactions that touch at least one of
	// the listed namespaces. Nil means no namespace filter.
	FilterNamespaces []string
	// FilterStatus limits events to transactions with at least one of the listed
	// statuses. Nil means no status filter. Because Status is coarser than the
	// underlying service's codes, a single entry may match several service codes
	// (for example StatusRejected covers duplicate-ID and all malformed variants).
	FilterStatus []Status
	// IncludeReadWriteSets requests that Namespaces (read/write sets) be populated
	// on each CommittedTxEvent.
	IncludeReadWriteSets bool
	// IncludeEndorsements requests that Endorsements be populated on each
	// CommittedTxEvent.
	IncludeEndorsements bool
	// IncludeMetadata requests that Metadata (events and input args) be populated
	// on each CommittedTxEvent.
	IncludeMetadata bool
}

// AllTxPeer is the interface for opening a StreamAllTransactions stream.
// It is satisfied by network/fabricx.Peer.
type AllTxPeer interface {
	StreamAllTransactions(ctx context.Context, req *StreamAllRequest, processor AllTxProcessor) error
}

// AllTxStreamer subscribes to all committed transactions via the Fabric-X sidecar's
// StreamAllTransactions RPC. It is the companion to Notifier: where Notifier tracks
// specific txIDs you submitted, AllTxStreamer delivers every committed transaction
// (optionally filtered by namespace or status) as a real-time event feed.
//
// Note: StreamAllTransactions is a real-time feed with no historical replay.
// It does not support starting from a past block number. For full history or
// world-state synchronisation, use network.Synchronizer instead.
type AllTxStreamer struct {
	peer     AllTxPeer
	handlers []AllTxHandler
	log      sdk.Logger
}

// NewAllTxStreamer creates an AllTxStreamer that delivers committed transaction
// batches to the registered handlers via the given peer.
func NewAllTxStreamer(peer AllTxPeer, handlers []AllTxHandler, log sdk.Logger) *AllTxStreamer {
	return &AllTxStreamer{peer: peer, handlers: handlers, log: log}
}

// Stream opens the StreamAllTransactions server-stream and drives handlers until
// the context is cancelled or an error occurs. req controls optional
// namespace/status filters and whether read/write sets or endorsements are included.
func (s *AllTxStreamer) Stream(ctx context.Context, req *StreamAllRequest) error {
	return s.peer.StreamAllTransactions(ctx, req, s)
}

// ProcessBatch implements AllTxProcessor. It calls each registered handler in order;
// the first handler error is returned and no further handlers are called.
func (s *AllTxStreamer) ProcessBatch(ctx context.Context, batch AllTxBatch) error {
	for _, h := range s.handlers {
		if err := h.HandleBatch(ctx, batch); err != nil {
			return fmt.Errorf("handle batch: %w", err)
		}
	}
	return nil
}
