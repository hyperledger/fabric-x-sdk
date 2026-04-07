/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
)

var (
	errSynchronizerNotStarted = errors.New("not started")
	errSynchronizerNotReady   = errors.New("not ready: still syncing")
	errSynchronizerRetrying   = errors.New("retrying after error")
	errSynchronizerStopped    = errors.New("stopped")
	errAlreadyStarted         = errors.New("already started")
)

// syncState represents the synchronizer's operational state.
type syncState int

const (
	stateNotStarted syncState = iota // Start() not yet called.
	stateStarting                    // Start() called; first connection attempt in progress.
	stateSyncing                     // Actively receiving and processing blocks.
	stateReady                       // Caught up with peer; ready to serve.
	stateRetrying                    // Error encountered; backing off before retry.
	stateStopped                     // Context canceled; permanently stopped.
)

func (s syncState) String() string {
	switch s {
	case stateNotStarted:
		return "not-started"
	case stateStarting:
		return "starting"
	case stateSyncing:
		return "syncing"
	case stateReady:
		return "ready"
	case stateRetrying:
		return "retrying"
	case stateStopped:
		return "stopped"
	default:
		return fmt.Sprintf("syncState(%d)", int(s))
	}
}

// Synchronizer connects to a committing peer to maintain a local copy of the world state.
//
// Lifecycle: call Start once with a context. When that context is canceled the synchronizer
// stops the monitor goroutine, closes the peer connection, and transitions to stateStopped.
// A stopped Synchronizer cannot be restarted; create a new instance instead.
type Synchronizer struct {
	db        BlockHeightReader
	peer      SyncPeer
	processor BlockProcessor
	log       sdk.Logger

	mu            sync.Mutex
	state         syncState
	lastSyncErr   error              // last error that caused a transition to stateRetrying
	monitorCancel context.CancelFunc // non-nil only while a monitor goroutine is running
	runCtx        context.Context    // set once in Start(); used by transition to derive the monitor context
}

// BlockHeightReader reads the last processed block number from a local store.
type BlockHeightReader interface {
	BlockNumber(context.Context) (uint64, error)
}

// SyncPeer is the remote peer used for block streaming and chain height queries.
type SyncPeer interface {
	SubscribeBlocks(context.Context, uint64, BlockProcessor) error
	BlockHeight(context.Context) (uint64, error)
	Close() error
}

// NewSynchronizer creates a new Synchronizer.
func NewSynchronizer(db BlockHeightReader, peer SyncPeer, processor BlockProcessor, logger sdk.Logger) (*Synchronizer, error) {
	if db == nil {
		return nil, errors.New("db required")
	}
	if peer == nil {
		return nil, errors.New("peer required")
	}
	return &Synchronizer{
		db:        db,
		peer:      peer,
		log:       logger,
		processor: processor,
	}, nil
}

// Start begins the synchronization loop. It blocks until ctx is canceled, at which point
// it stops the monitor goroutine, closes the peer connection, transitions to stateStopped,
// and returns nil. May only be called once per Synchronizer instance.
func (s *Synchronizer) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.state != stateNotStarted {
		s.mu.Unlock()
		return errAlreadyStarted
	}
	s.state = stateStarting
	s.runCtx = ctx
	s.mu.Unlock()

	defer func() {
		s.transition(stateStopped, nil)
		if err := s.peer.Close(); err != nil {
			s.log.Warnf("peer close: %v", err)
		}
	}()

	currentBackoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return nil
		}

		lastBlock, err := s.db.BlockNumber(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.transition(stateRetrying, err)
			s.log.Warnf("failed to read block height from DB: %v — retrying in %s", err, currentBackoff)
			if err := sleepCtx(ctx, currentBackoff); err != nil {
				return nil
			}
			currentBackoff = min(currentBackoff*2, maxBackoff)
			continue
		}

		// BlockNumber returns 0 both when the store is fresh and when block 0 was the
		// last block processed. Starting from 0 is safe in both cases — the genesis
		// block carries no state-changing transactions.
		var start uint64
		if lastBlock > 0 {
			start = lastBlock + 1
		}

		s.log.Infof("starting synchronization from block %d...", start)
		s.transition(stateSyncing, nil) // clears lastSyncErr, starts the monitor goroutine

		err = s.peer.SubscribeBlocks(ctx, start, s.processor)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.transition(stateRetrying, err)
			s.log.Warnf("deliver error: %v — retrying in %s", err, currentBackoff)
			if err := sleepCtx(ctx, currentBackoff); err != nil {
				return nil
			}
			currentBackoff = min(currentBackoff*2, maxBackoff)
			continue
		}
		currentBackoff = time.Second
	}
}

// transition atomically moves the synchronizer to the given state and applies all side effects.
//
// Exit effects (on leaving a state):
//   - stateSyncing → any: monitor goroutine is canceled.
//
// Entry effects (on entering a state):
//   - stateSyncing: clears lastSyncErr; starts a new monitor goroutine.
//   - stateRetrying: stores err as lastSyncErr.
//   - stateStopped: cancels the monitor goroutine if still running.
//
// Transitions out of stateStopped are silently ignored (terminal state).
func (s *Synchronizer) transition(to syncState, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	from := s.state
	if from == stateStopped {
		return
	}
	s.state = to
	s.log.Debugf("%s -> %s", from, to)

	// Exit effects.
	if from == stateSyncing && s.monitorCancel != nil {
		s.monitorCancel()
		s.monitorCancel = nil
	}

	// Entry effects.
	switch to {
	case stateSyncing:
		s.lastSyncErr = nil
		if s.runCtx != nil && s.runCtx.Err() != nil {
			return // already canceled again, no need to monitor
		}
		monCtx, cancel := context.WithCancel(s.runCtx)
		s.monitorCancel = cancel
		go s.monitorReadiness(monCtx)

	case stateRetrying:
		s.lastSyncErr = err

	case stateStopped:
		if s.monitorCancel != nil {
			s.monitorCancel()
			s.monitorCancel = nil
		}
	}
}

// BlockHeight returns the block height, i.e. the index of the next block to be processed.
func (s *Synchronizer) BlockHeight(ctx context.Context) (uint64, error) {
	lpb, err := s.db.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	return lpb + 1, nil
}

// PeerBlockHeight returns the peer's current block height.
func (s *Synchronizer) PeerBlockHeight(ctx context.Context) (uint64, error) {
	return s.peer.BlockHeight(ctx)
}

// Live reports whether the synchronizer's sync loop is operational.
// Returns nil for stateStarting, stateSyncing, and stateReady.
// Analogous to a Kubernetes liveness probe.
func (s *Synchronizer) Live() error {
	s.mu.Lock()
	state := s.state
	syncErr := s.lastSyncErr
	s.mu.Unlock()

	switch state {
	case stateNotStarted:
		return errSynchronizerNotStarted
	case stateStopped:
		return errSynchronizerStopped
	case stateRetrying:
		if syncErr != nil {
			return fmt.Errorf("%w: %v", errSynchronizerRetrying, syncErr)
		}
		return errSynchronizerRetrying
	default:
		// stateStarting, stateSyncing, stateReady
		return nil
	}
}

// Ready reports whether the synchronizer has caught up with the peer.
// Returns nil only in stateReady.
// Analogous to a Kubernetes readiness probe.
func (s *Synchronizer) Ready() error {
	s.mu.Lock()
	state := s.state
	syncErr := s.lastSyncErr
	s.mu.Unlock()

	switch state {
	case stateNotStarted:
		return errSynchronizerNotStarted
	case stateStopped:
		return errSynchronizerStopped
	case stateStarting, stateSyncing:
		return errSynchronizerNotReady
	case stateRetrying:
		if syncErr != nil {
			return fmt.Errorf("%w: %v", errSynchronizerRetrying, syncErr)
		}
		return errSynchronizerRetrying
	case stateReady:
		return nil
	default:
		return errSynchronizerNotReady
	}
}

// getState returns the current state under the mutex.
func (s *Synchronizer) getState() syncState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// monitorReadiness periodically checks whether the local chain has caught up with the peer.
// On catching up it transitions to stateReady (which cancels its own context) and returns.
//
// The goroutine is started exclusively by transition(stateSyncing) and lives exactly as long
// as the syncing period — its context is canceled on any exit from stateSyncing.
func (s *Synchronizer) monitorReadiness(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			peerHeight, err := s.peer.BlockHeight(ctx)
			if err != nil {
				s.log.Debugf("peer height check failed: %v", err)
				continue
			}

			localLast, err := s.BlockHeight(ctx)
			if err != nil {
				s.log.Debugf("local height read failed: %v", err)
				continue
			}

			if localLast >= peerHeight {
				s.transition(stateReady, nil)
				s.log.Infof("synchronizer ready at block %d", localLast)
				return
			}
		}
	}
}

// sleepCtx sleeps for d or returns early if ctx is canceled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
