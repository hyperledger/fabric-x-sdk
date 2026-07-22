/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
)

// FinalityProvider subscribes to transaction finality events.
// It is satisfied by FinalityListener and accepted by network.Submitter.
//
// Register must be called before broadcasting the transaction so that a
// committed notification is never missed.
type FinalityProvider interface {
	// Register adds txID to the pending set and returns a channel that will
	// receive exactly one TxStatusEvent when the transaction reaches finality.
	// The returned channel is buffered (capacity 1).
	//
	// Register blocks until the subscription has been queued for the underlying
	// stream or ctx is done; it never silently drops a subscription. It returns
	// ErrAlreadyRegistered if txID is already pending.
	//
	// The caller owns the returned channel and must either receive from it or
	// call Unregister to release the entry; otherwise the entry leaks until the
	// provider is shut down.
	Register(ctx context.Context, txID string) (<-chan TxStatusEvent, error)

	// Unregister removes a pending txID. No-op if not present.
	// Call when a broadcast fails or the caller's context expires.
	Unregister(txID string)
}

// StatusQuerier synchronously fetches the current finality status of a set of
// transactions. When a FinalityListener's peer also implements StatusQuerier,
// the listener reconciles its pending transactions on every (re)connect, so a
// status that became final while the stream was down is delivered rather than
// waited on until the caller's context expires. Implementations should report a
// not-yet-final transaction with StatusUnknown (or omit it). It is satisfied by
// network/fabricx.Peer.
type StatusQuerier interface {
	GetTransactionStatus(ctx context.Context, txIDs []string) ([]TxStatusEvent, error)
}

// ErrAlreadyRegistered is returned by FinalityProvider.Register when txID is
// already registered and awaiting finality. A duplicate registration almost
// always indicates a caller bug, since transaction IDs are unique per submission.
var ErrAlreadyRegistered = errors.New("txID already registered")

// ErrTxRejected describes a transaction that was broadcast but did not commit.
// SubmitAndWait returns the raw TxStatusEvent (a non-committed status is not an
// error); callers that prefer an error can build one from a non-valid event, for
// example: if !event.Valid() { return &ErrTxRejected{event.TxID, event.Status, event.Reason} }.
// Reason carries the raw, service-specific status label when available.
type ErrTxRejected struct {
	TxID   string
	Status Status
	Reason string
}

func (e *ErrTxRejected) Error() string {
	reason := e.Reason
	if reason == "" {
		reason = e.Status.String()
	}
	return fmt.Sprintf("transaction %s rejected: %s", e.TxID, reason)
}

// FinalityListener manages a long-lived, reconnecting notification stream and
// multiplexes per-txID finality waits over it. It implements FinalityProvider.
//
// Call Start in a goroutine before the first Register call:
//
//	go listener.Start(ctx)
//
// Start reconnects automatically on stream failure using exponential backoff
// (500ms initial, capped at 30s). On every (re)connect all pending txIDs are
// re-subscribed and, when the peer implements StatusQuerier, reconciled against
// the committer's current state, so a status that became final while the stream
// was down is still delivered.
type FinalityListener struct {
	peer    NotificationPeer
	timeout time.Duration
	log     sdk.Logger

	mu      sync.Mutex
	pending map[string]chan TxStatusEvent
	// subCh carries subscription batches to the active stream: Register writes
	// individual txIDs and each (re)connect writes the pending set, while the
	// running peer.Notify drains it. Buffered, so a full channel applies
	// backpressure to Register instead of dropping subscriptions.
	subCh chan []string
}

// NewFinalityListener creates a FinalityListener that uses peer for the
// notification stream. timeout is the per-request timeout passed to the sidecar
// (how long the sidecar will hold a subscription open); a zero timeout tells the
// committer to apply its own configured default.
func NewFinalityListener(peer NotificationPeer, timeout time.Duration, log sdk.Logger) *FinalityListener {
	return &FinalityListener{
		peer:    peer,
		timeout: timeout,
		log:     log,
		pending: make(map[string]chan TxStatusEvent),
		subCh:   make(chan []string, 256),
	}
}

// Start runs the reconnecting notification stream loop until ctx is cancelled.
// It must be called (typically as go listener.Start(ctx)) before the first
// Register call to ensure subscriptions are forwarded to the sidecar.
func (f *FinalityListener) Start(ctx context.Context) {
	const (
		initBackoff = 500 * time.Millisecond
		maxBackoff  = 30 * time.Second
	)
	backoff := initBackoff

	for ctx.Err() == nil {
		if err := f.runSession(ctx); err != nil && ctx.Err() == nil {
			f.log.Warnf("finality notification stream error: %v; reconnecting in %v", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
		} else {
			backoff = initBackoff // reset after a clean close
		}
	}
}

// runSession opens one notification stream session. It returns when the stream
// closes (nil on clean context cancellation, non-nil on error).
func (f *FinalityListener) runSession(ctx context.Context) error {
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()

	// Re-subscribe and reconcile pending txIDs concurrently with the stream, so
	// stream setup is not delayed and the sends cannot deadlock on subCh.
	go f.onConnect(ctx2)

	processor := NewProcessor([]TxStatusHandler{f}, f.log)
	return f.peer.Notify(ctx2, f.subCh, processor, f.timeout)
}

// onConnect re-subscribes all currently pending txIDs to the freshly
// (re)connected stream and reconciles their status, sharing a single snapshot.
// Re-subscription makes the stream report their future finality; reconciliation
// recovers any status that became final while the stream was down.
func (f *FinalityListener) onConnect(ctx context.Context) {
	f.mu.Lock()
	pending := make([]string, 0, len(f.pending))
	for txID := range f.pending {
		pending = append(pending, txID)
	}
	f.mu.Unlock()
	if len(pending) == 0 {
		return
	}

	// Re-subscribe so the stream will notify us of future finality. Sent from
	// here (not before Notify) so the concurrent stream drains subCh; this can
	// therefore never deadlock on a full channel.
	select {
	case f.subCh <- pending:
	case <-ctx.Done():
		return
	}

	f.reconcile(ctx, pending)
}

// reconcile queries the current status of the given transactions and delivers
// any that have already reached finality. This closes the gap where a
// transaction commits (or is rejected) while the notification stream is down:
// re-subscription alone is not guaranteed by the sidecar to replay a status that
// became final during the outage. It is a no-op unless f.peer implements
// StatusQuerier.
func (f *FinalityListener) reconcile(ctx context.Context, txIDs []string) {
	q, ok := f.peer.(StatusQuerier)
	if !ok {
		return
	}

	events, err := q.GetTransactionStatus(ctx, txIDs)
	if err != nil {
		f.log.Warnf("finality listener: status reconciliation failed: %v", err)
		return
	}

	// Only deliver terminal statuses; a still-pending tx must keep waiting for a
	// stream event rather than be resolved with a non-final status.
	final := make([]TxStatusEvent, 0, len(events))
	for _, e := range events {
		if e.Status.IsFinal() {
			final = append(final, e)
		}
	}
	if len(final) > 0 {
		_ = f.Handle(ctx, final)
	}
}

// Register implements FinalityProvider. It adds txID to the pending set and
// queues a subscription to the current notification stream.
// Call Register before broadcasting the transaction.
func (f *FinalityListener) Register(ctx context.Context, txID string) (<-chan TxStatusEvent, error) {
	responseCh := make(chan TxStatusEvent, 1)

	f.mu.Lock()
	if _, exists := f.pending[txID]; exists {
		f.mu.Unlock()
		return nil, fmt.Errorf("%w: %s", ErrAlreadyRegistered, txID)
	}
	f.pending[txID] = responseCh
	f.mu.Unlock()

	// Send the subscription straight to the active stream's channel. Blocks only
	// when the buffer is full (backpressure) and never silently drops; txID is
	// also in pending, so a concurrent (re)connect re-subscribes it as well.
	select {
	case f.subCh <- []string{txID}:
		return responseCh, nil
	case <-ctx.Done():
		f.Unregister(txID)
		return nil, ctx.Err()
	}
}

// Unregister implements FinalityProvider. It removes txID from the pending set.
// No-op if txID is not present.
func (f *FinalityListener) Unregister(txID string) {
	f.mu.Lock()
	delete(f.pending, txID)
	f.mu.Unlock()
}

// WaitForFinality is a convenience wrapper around Register that blocks until
// the transaction reaches finality or ctx expires.
//
// For correct use with a Submitter, prefer SubmitAndWait which calls Register
// before broadcasting. WaitForFinality is intended for standalone use where the
// caller manages broadcasting separately.
//
// A sidecar-side subscription timeout releases the wait with a StatusUnknown
// event and a nil error; this is not a final outcome (see SubmitAndWait for the
// full caveat). Inspect event.Status, not only event.Valid().
func (f *FinalityListener) WaitForFinality(ctx context.Context, txID string) (TxStatusEvent, error) {
	ch, err := f.Register(ctx, txID)
	if err != nil {
		return TxStatusEvent{}, err
	}
	select {
	case event := <-ch:
		return event, nil
	case <-ctx.Done():
		f.Unregister(txID)
		return TxStatusEvent{}, ctx.Err()
	}
}

// Handle implements TxStatusHandler. It is called by the Processor for each
// batch of events received from the notification stream.
func (f *FinalityListener) Handle(_ context.Context, events []TxStatusEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range events {
		if ch, ok := f.pending[e.TxID]; ok {
			ch <- e // buffered(1): never blocks
			delete(f.pending, e.TxID)
		}
	}
	return nil
}
