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
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// CommittedTxEvent is a single transaction event received from StreamAllTransactions.
// It embeds blocks.Transaction, so InputArgs/Events/NsRWS are populated only
// when IncludeMetadata/IncludeReadWriteSets were set in the StreamAllRequest,
// and Status/RawCode/Reason/Valid always reflect the event's outcome.
type CommittedTxEvent struct {
	blocks.Transaction
	// BlockNum is the number of the block this transaction committed in.
	// Transaction.Number is the transaction's index within that block.
	BlockNum uint64
	// Endorsements is populated only when IncludeEndorsements was set in the
	// StreamAllRequest.
	Endorsements []*applicationpb.Endorsements
}

// AllTxBatch is a batch of transaction events from a single committed block.
// Preserving block boundaries allows handlers to process a whole block atomically.
type AllTxBatch struct {
	BlockNumber uint64
	Events      []CommittedTxEvent
}

// AllTxHandler processes batches of committed transaction events.
// Handlers are invoked sequentially for each block's worth of events, in the order
// the blocks were received.
//
// HandleBatch does not run on the stream's receive goroutine: AllTxStreamer runs the
// handler chain on a goroutine of its own and buffers up to AllTxQueueDepth batches
// between the two, so a handler may take as long as a block's worth of work needs
// without stalling the feed. A chain that is slower than the feed on average will
// still fill that buffer and apply backpressure to the stream — see AllTxQueueDepth.
type AllTxHandler interface {
	HandleBatch(ctx context.Context, batch AllTxBatch) error
}

// AllTxProcessor is the internal callback interface used by fabricx.Peer.StreamAllTransactions.
// It is satisfied by the queue AllTxStreamer hands to the peer for each Stream call.
//
// ProcessBatch is called from the stream's receive loop, sequentially and in block
// order. Implementations of AllTxPeer must not call it after StreamAllTransactions
// has returned.
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
	// statuses. Nil means no status filter. Most Status values map to a single
	// service code, but StatusMalformed is coarser (it matches every malformed-
	// envelope variant the underlying service reports) and
	// StatusEndorsementPolicyFailure maps onto the same code as
	// StatusInvalidSignature, since the Fabric-X committer doesn't distinguish
	// the two. StatusUnrecognized names no fixed service code, so filtering a
	// stream by it alone matches nothing.
	FilterStatus []blocks.Status
	// IncludeReadWriteSets requests that NsRWS (read/write sets) be populated
	// on each CommittedTxEvent.
	IncludeReadWriteSets bool
	// IncludeEndorsements requests that Endorsements be populated on each
	// CommittedTxEvent.
	IncludeEndorsements bool
	// IncludeMetadata requests that InputArgs and Events be populated on each
	// CommittedTxEvent.
	IncludeMetadata bool
}

// AllTxPeer is the interface for opening a StreamAllTransactions stream.
// It is satisfied by network/fabricx.Peer.
type AllTxPeer interface {
	StreamAllTransactions(ctx context.Context, req *StreamAllRequest, processor AllTxProcessor) error
}

// AllTxQueueDepth is how many committed-block batches AllTxStreamer buffers between
// the stream's receive loop and the goroutine running the handler chain.
//
// The buffer absorbs bursts and the per-block variance of the handler chain; it is
// not there to hide a chain that is too slow. Once it is full the receive loop blocks
// until the handlers catch up. That backpressure is deliberate: StreamAllTransactions
// has no historical replay, so a dropped batch could never be recovered, which makes
// slowing the stream down the only safe response to a persistently slow handler.
const AllTxQueueDepth = 64

// AllTxStreamer subscribes to all committed transactions via the Fabric-X sidecar's
// StreamAllTransactions RPC. It is the companion to Notifier: where Notifier tracks
// specific txIDs you submitted, AllTxStreamer delivers every committed transaction
// (optionally filtered by namespace or status) as a real-time event feed.
//
// Receiving and processing are decoupled: the peer's receive loop only enqueues
// batches, and the registered handlers run in block order on a single goroutine that
// Stream owns. Handlers that apply blocks to a store therefore do not hold up the
// gRPC stream, and the stream keeps draining while a block is being applied.
//
// Note: StreamAllTransactions is a real-time feed with no historical replay.
// It does not support starting from a past block number. For full history or
// world-state synchronisation, use network.Synchronizer instead.
type AllTxStreamer struct {
	peer       AllTxPeer
	handlers   []AllTxHandler
	log        sdk.Logger
	queueDepth int
}

// NewAllTxStreamer creates an AllTxStreamer that delivers committed transaction
// batches to the registered handlers via the given peer.
// queueDepth controls how many committed-block batches are buffered between the
// stream's receive loop and the handler goroutine; pass 0 to use AllTxQueueDepth.
func NewAllTxStreamer(peer AllTxPeer, handlers []AllTxHandler, log sdk.Logger, queueDepth int) *AllTxStreamer {
	if queueDepth <= 0 {
		queueDepth = AllTxQueueDepth
	}
	return &AllTxStreamer{peer: peer, handlers: handlers, log: log, queueDepth: queueDepth}
}

// Stream opens the StreamAllTransactions server-stream and drives handlers until
// the context is cancelled or an error occurs. req controls optional
// namespace/status filters and whether read/write sets or endorsements are included.
//
// Stream runs the handler chain on a goroutine it owns rather than on the peer's
// receive loop, and does not return until that goroutine has exited. It returns the
// first handler error if there was one, nil if ctx was cancelled (a normal shutdown),
// otherwise whatever ended the stream.
func (s *AllTxStreamer) Stream(ctx context.Context, req *StreamAllRequest) error {
	// Cancelling streamCtx is how the handler goroutine tears the stream down after a
	// handler error, and what releases a receive loop blocked on a full queue.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	queue := make(chan AllTxBatch, s.queueDepth)

	handlerDone := make(chan struct{})
	var handlerErr error
	go func() {
		defer close(handlerDone)
		handlerErr = s.runHandlers(streamCtx, queue)
		// Whether it stopped on an error or on shutdown, nothing will drain the queue
		// from here on, so stop the receive loop before it blocks on a full one.
		cancel()
	}()

	processor := &allTxQueue{batches: queue, done: streamCtx.Done(), log: s.log, queueDepth: s.queueDepth}
	streamErr := s.peer.StreamAllTransactions(streamCtx, req, processor)

	// The receive loop has returned, so by the AllTxProcessor contract nothing can
	// enqueue any more. Closing the queue lets the handler goroutine finish the
	// batches still buffered in it and exit.
	close(queue)
	<-handlerDone

	switch {
	case handlerErr != nil:
		return handlerErr
	case ctx.Err() != nil:
		// Cancelled by our caller: a normal shutdown, not a stream failure. Any
		// streamErr here is just the receive loop noticing the same cancellation.
		return nil
	default:
		return streamErr
	}
}

// runHandlers drains queue and runs the handler chain over each batch in the order it
// was received, stopping at the first handler error. It returns nil once the queue is
// closed and fully drained (the stream ended cleanly) or ctx is done (shutdown).
func (s *AllTxStreamer) runHandlers(ctx context.Context, queue <-chan AllTxBatch) error {
	for {
		// Checked ahead of the receive so that on shutdown a cancelled ctx wins over a
		// queue that still holds batches: select would otherwise pick between the two
		// at random, and there is no point applying more blocks once we are stopping.
		if ctx.Err() != nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case batch, ok := <-queue:
			if !ok {
				return nil // stream ended and the queue is drained
			}
			for _, h := range s.handlers {
				if err := h.HandleBatch(ctx, batch); err != nil {
					return fmt.Errorf("handle batch %d: %w", batch.BlockNumber, err)
				}
			}
		}
	}
}

// allTxQueue is the AllTxProcessor handed to the peer for a single Stream call. It
// implements the hand-off described on AllTxStreamer: the receive loop only enqueues,
// so the cost of the handler chain is never charged to it.
type allTxQueue struct {
	batches chan<- AllTxBatch
	// done is closed when the stream is being torn down, by Stream's caller or by the
	// handler goroutine after a handler error. Without it an enqueue onto a full queue
	// would block forever once nothing is draining it.
	done       <-chan struct{}
	log        sdk.Logger
	queueDepth int
}

// ProcessBatch implements AllTxProcessor by handing the batch to the handler
// goroutine. It blocks only while the queue is full.
func (q *allTxQueue) ProcessBatch(ctx context.Context, batch AllTxBatch) error {
	select {
	case q.batches <- batch:
		return nil
	default:
	}

	// Full queue: the handler chain is not keeping up with the feed, so from here the
	// stream runs at the handlers' pace. Worth saying out loud — it is the one case
	// where a slow handler is visible as sidecar-side backpressure.
	q.log.Warnf("all-tx queue full (%d batches) at block %d: waiting for handlers", q.queueDepth, batch.BlockNumber)

	select {
	case q.batches <- batch:
		return nil
	case <-q.done:
		return context.Canceled
	case <-ctx.Done():
		return ctx.Err()
	}
}
