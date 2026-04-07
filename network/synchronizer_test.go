/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	sdk "github.com/hyperledger/fabric-x-sdk"
)

// Test-only methods on *Synchronizer.
// These bypass transition effects and are only for setting up health-check scenarios.

func (s *Synchronizer) setState(state syncState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}

func (s *Synchronizer) getLastSyncErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSyncErr
}

func (s *Synchronizer) setLastSyncErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSyncErr = err
}

// Mock implementations

type mockBlockHeightReader struct {
	mu          sync.Mutex
	blockNumber uint64
	err         error
	callCount   int
}

func (m *mockBlockHeightReader) BlockNumber(ctx context.Context) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	if m.err != nil {
		return 0, m.err
	}
	return m.blockNumber, nil
}

func (m *mockBlockHeightReader) setError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

func (m *mockBlockHeightReader) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

type mockSyncPeer struct {
	mu                sync.Mutex
	subscribeErr      error
	blockHeightValue  uint64
	blockHeightErr    error
	closeErr          error
	subscribeCalls    int
	blockHeightCalls  int
	closeCalls        int
	subscribeBlocking bool // if true, SubscribeBlocks blocks until ctx is canceled
}

func (m *mockSyncPeer) SubscribeBlocks(ctx context.Context, start uint64, processor BlockProcessor) error {
	m.mu.Lock()
	m.subscribeCalls++
	err := m.subscribeErr
	blocking := m.subscribeBlocking
	m.mu.Unlock()

	if blocking {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (m *mockSyncPeer) BlockHeight(ctx context.Context) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blockHeightCalls++
	if m.blockHeightErr != nil {
		return 0, m.blockHeightErr
	}
	return m.blockHeightValue, nil
}

func (m *mockSyncPeer) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCalls++
	return m.closeErr
}

func (m *mockSyncPeer) setSubscribeError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribeErr = err
}

func (m *mockSyncPeer) getSubscribeCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.subscribeCalls
}

type mockBlockProcessor struct {
	mu        sync.Mutex
	callCount int
	err       error
}

func (m *mockBlockProcessor) ProcessBlock(ctx context.Context, block *common.Block) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return m.err
}

// Constructor tests

func TestNewSynchronizer(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		s, err := NewSynchronizer(&mockBlockHeightReader{}, &mockSyncPeer{}, &mockBlockProcessor{}, sdk.NoOpLogger{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s == nil {
			t.Fatal("expected non-nil synchronizer")
		}
		if s.getState() != stateNotStarted {
			t.Errorf("expected %v, got %v", stateNotStarted, s.getState())
		}
	})

	t.Run("nil db returns error", func(t *testing.T) {
		s, err := NewSynchronizer(nil, &mockSyncPeer{}, &mockBlockProcessor{}, sdk.NoOpLogger{})
		if err == nil {
			t.Fatal("expected error for nil db")
		}
		if s != nil {
			t.Fatal("expected nil synchronizer on error")
		}
	})

	t.Run("nil peer returns error", func(t *testing.T) {
		s, err := NewSynchronizer(&mockBlockHeightReader{}, nil, &mockBlockProcessor{}, sdk.NoOpLogger{})
		if err == nil {
			t.Fatal("expected error for nil peer")
		}
		if s != nil {
			t.Fatal("expected nil synchronizer on error")
		}
	})
}

// State transition tests

func TestSynchronizer_StateTransitions(t *testing.T) {
	t.Run("initial state is not started", func(t *testing.T) {
		s := createTestSynchronizer(t)
		if s.getState() != stateNotStarted {
			t.Errorf("expected %v, got %v", stateNotStarted, s.getState())
		}
	})

	t.Run("start transitions to starting then syncing", func(t *testing.T) {
		// blockHeightValue far ahead so the monitor never catches up during this test.
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // Start blocks in SubscribeBlocks; monitor blocks on ticker.

			if s.getState() != stateSyncing {
				t.Errorf("expected stateSyncing, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("double start returns errAlreadyStarted", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // in stateSyncing

			if err := s.Start(ctx); !errors.Is(err, errAlreadyStarted) {
				t.Errorf("expected errAlreadyStarted, got %v", err)
			}
			cancel()
		})
	})

	t.Run("subscribe error transitions to retrying", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeErr: errors.New("subscribe failed")}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			// Start: db OK → syncing → subscribe error → retrying → sleeping 1 s backoff.
			synctest.Wait()

			if s.getState() != stateRetrying {
				t.Errorf("expected stateRetrying, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("db error transitions to retrying", func(t *testing.T) {
		db := &mockBlockHeightReader{err: errors.New("db error")}
		s := createTestSynchronizerWithMocks(t, db, &mockSyncPeer{})

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // Start: db error → retrying → sleeping 1 s backoff.

			if s.getState() != stateRetrying {
				t.Errorf("expected stateRetrying, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("syncing transitions to ready when caught up", func(t *testing.T) {
		// localHeight = blockNumber+1 = 11, peerHeight = 11 → caught up on first monitor tick.
		db := &mockBlockHeightReader{blockNumber: 10}
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 11}
		s := createTestSynchronizerWithMocks(t, db, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait()         // Start in syncing; monitor on 1 s ticker.
			time.Sleep(time.Second) // advance fake clock 1 s → monitor tick fires.
			synctest.Wait()         // monitor transitions to ready and exits.

			if s.getState() != stateReady {
				t.Errorf("expected stateReady, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("context cancellation transitions to stopped", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // in stateSyncing
			cancel()
			synctest.Wait() // Start and monitor exit; state = stopped.

			if s.getState() != stateStopped {
				t.Errorf("expected stateStopped, got %v", s.getState())
			}
		})
	})

	t.Run("stopped synchronizer rejects second Start", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait()
			cancel()
			synctest.Wait() // stopped

			if err := s.Start(t.Context()); !errors.Is(err, errAlreadyStarted) {
				t.Errorf("expected errAlreadyStarted, got %v", err)
			}
		})
	})

	t.Run("peer is closed when Start returns", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait()
			cancel()
			synctest.Wait() // Start has exited; peer.Close() was called in defer.

			peer.mu.Lock()
			n := peer.closeCalls
			peer.mu.Unlock()

			if n != 1 {
				t.Errorf("expected 1 peer.Close() call, got %d", n)
			}
		})
	})
}

// Error handling tests

func TestSynchronizer_ErrorHandling(t *testing.T) {
	t.Run("retries on db error with exponential backoff", func(t *testing.T) {
		db := &mockBlockHeightReader{err: errors.New("db error")}
		s := createTestSynchronizerWithMocks(t, db, &mockSyncPeer{})

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			// Attempt 1: db fails, Start sleeps 1 s.
			synctest.Wait()
			// Advance past the 1 s backoff → attempt 2: db fails, Start sleeps 2 s.
			time.Sleep(time.Second)
			synctest.Wait()

			if db.getCallCount() < 2 {
				t.Errorf("expected ≥ 2 db calls, got %d", db.getCallCount())
			}
			if s.getState() != stateRetrying {
				t.Errorf("expected stateRetrying, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("retries on subscribe error with exponential backoff", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeErr: errors.New("subscribe error")}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			// Attempt 1: subscribe fails, Start sleeps 1 s.
			synctest.Wait()
			// Advance past 1 s → attempt 2: subscribe fails, Start sleeps 2 s.
			time.Sleep(time.Second)
			synctest.Wait()

			if peer.getSubscribeCalls() < 2 {
				t.Errorf("expected ≥ 2 subscribe calls, got %d", peer.getSubscribeCalls())
			}
			if s.getState() != stateRetrying {
				t.Errorf("expected stateRetrying, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("recovers from transient db error", func(t *testing.T) {
		db := &mockBlockHeightReader{err: errors.New("transient db error")}
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, db, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // db error → retrying → sleeping 1 s.
			if s.getState() != stateRetrying {
				t.Errorf("expected stateRetrying, got %v", s.getState())
			}

			db.setError(nil)

			// Advance past the 1 s backoff → db OK → syncing.
			time.Sleep(time.Second)
			synctest.Wait()

			if s.getState() != stateSyncing {
				t.Errorf("expected stateSyncing after recovery, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("recovers from transient subscribe error", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeErr: errors.New("transient subscribe error"), blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // subscribe error → retrying → sleeping 1 s.
			if s.getState() != stateRetrying {
				t.Errorf("expected stateRetrying, got %v", s.getState())
			}

			peer.setSubscribeError(nil)
			peer.mu.Lock()
			peer.subscribeBlocking = true
			peer.mu.Unlock()

			// Advance past the 1 s backoff → subscribe OK → syncing.
			time.Sleep(time.Second)
			synctest.Wait()

			if s.getState() != stateSyncing {
				t.Errorf("expected stateSyncing after recovery, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("context cancellation stops retry loop", func(t *testing.T) {
		db := &mockBlockHeightReader{err: errors.New("persistent db error")}
		s := createTestSynchronizerWithMocks(t, db, &mockSyncPeer{})

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // in retrying, sleeping 1 s backoff.
			cancel()        // cancels the sleep → Start returns nil immediately.
			synctest.Wait() // Start and its defer have completed.
		})
	})

	t.Run("lastSyncErr is set on db error", func(t *testing.T) {
		expectedErr := errors.New("specific db error")
		db := &mockBlockHeightReader{err: expectedErr}
		s := createTestSynchronizerWithMocks(t, db, &mockSyncPeer{})

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait()

			if got := s.getLastSyncErr(); got == nil || got.Error() != expectedErr.Error() {
				t.Errorf("expected lastSyncErr %v, got %v", expectedErr, got)
			}
			cancel()
		})
	})

	t.Run("lastSyncErr is set on subscribe error", func(t *testing.T) {
		expectedErr := errors.New("specific subscribe error")
		peer := &mockSyncPeer{subscribeErr: expectedErr}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait()

			if got := s.getLastSyncErr(); got == nil || got.Error() != expectedErr.Error() {
				t.Errorf("expected lastSyncErr %v, got %v", expectedErr, got)
			}
			cancel()
		})
	})

	t.Run("lastSyncErr is cleared on recovery to syncing", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeErr: errors.New("transient"), blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // subscribe error → retrying

			peer.setSubscribeError(nil)
			peer.mu.Lock()
			peer.subscribeBlocking = true
			peer.mu.Unlock()

			time.Sleep(time.Second)
			synctest.Wait() // recovered → syncing

			if got := s.getLastSyncErr(); got != nil {
				t.Errorf("expected nil lastSyncErr after recovery, got %v", got)
			}
			cancel()
		})
	})
}

// Live tests

func TestSynchronizer_Live(t *testing.T) {
	t.Run("not started returns errSynchronizerNotStarted", func(t *testing.T) {
		s := createTestSynchronizer(t)
		if !errors.Is(s.Live(), errSynchronizerNotStarted) {
			t.Errorf("expected errSynchronizerNotStarted, got %v", s.Live())
		}
	})

	t.Run("starting is live", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateStarting)
		if err := s.Live(); err != nil {
			t.Errorf("expected nil for starting, got %v", err)
		}
	})

	t.Run("syncing is live", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateSyncing)
		if err := s.Live(); err != nil {
			t.Errorf("expected nil for syncing, got %v", err)
		}
	})

	t.Run("ready is live", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateReady)
		if err := s.Live(); err != nil {
			t.Errorf("expected nil for ready, got %v", err)
		}
	})

	t.Run("retrying returns errSynchronizerRetrying", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateRetrying)
		if !errors.Is(s.Live(), errSynchronizerRetrying) {
			t.Errorf("expected errSynchronizerRetrying, got %v", s.Live())
		}
	})

	t.Run("retrying with lastSyncErr wraps the error", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateRetrying)
		s.setLastSyncErr(errors.New("specific error"))

		err := s.Live()
		if !errors.Is(err, errSynchronizerRetrying) {
			t.Errorf("expected errSynchronizerRetrying, got %v", err)
		}
		if err.Error() != "retrying after error: specific error" {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("stopped returns errSynchronizerStopped", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateStopped)
		if !errors.Is(s.Live(), errSynchronizerStopped) {
			t.Errorf("expected errSynchronizerStopped, got %v", s.Live())
		}
	})
}

// Ready tests (no timing — no synctest needed)

func TestSynchronizer_Ready(t *testing.T) {
	t.Run("not started returns errSynchronizerNotStarted", func(t *testing.T) {
		s := createTestSynchronizer(t)
		if !errors.Is(s.Ready(), errSynchronizerNotStarted) {
			t.Errorf("expected errSynchronizerNotStarted, got %v", s.Ready())
		}
	})

	t.Run("starting is not ready", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateStarting)
		if !errors.Is(s.Ready(), errSynchronizerNotReady) {
			t.Errorf("expected errSynchronizerNotReady, got %v", s.Ready())
		}
	})

	t.Run("syncing is not ready", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateSyncing)
		if !errors.Is(s.Ready(), errSynchronizerNotReady) {
			t.Errorf("expected errSynchronizerNotReady, got %v", s.Ready())
		}
	})

	t.Run("ready returns nil", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateReady)
		if err := s.Ready(); err != nil {
			t.Errorf("expected nil for ready, got %v", err)
		}
	})

	t.Run("retrying returns errSynchronizerRetrying", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateRetrying)
		if !errors.Is(s.Ready(), errSynchronizerRetrying) {
			t.Errorf("expected errSynchronizerRetrying, got %v", s.Ready())
		}
	})

	t.Run("retrying with lastSyncErr wraps the error", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateRetrying)
		s.setLastSyncErr(errors.New("specific error"))

		err := s.Ready()
		if !errors.Is(err, errSynchronizerRetrying) {
			t.Errorf("expected errSynchronizerRetrying, got %v", err)
		}
		if err.Error() != "retrying after error: specific error" {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("stopped returns errSynchronizerStopped", func(t *testing.T) {
		s := createTestSynchronizer(t)
		s.setState(stateStopped)
		if !errors.Is(s.Ready(), errSynchronizerStopped) {
			t.Errorf("expected errSynchronizerStopped, got %v", s.Ready())
		}
	})
}

// BlockHeight tests

func TestSynchronizer_BlockHeight(t *testing.T) {
	t.Run("returns db block number plus one", func(t *testing.T) {
		db := &mockBlockHeightReader{blockNumber: 42}
		s := createTestSynchronizerWithMocks(t, db, &mockSyncPeer{})
		height, err := s.BlockHeight(t.Context())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if height != 43 {
			t.Errorf("expected 43, got %d", height)
		}
	})

	t.Run("returns error from db", func(t *testing.T) {
		db := &mockBlockHeightReader{err: errors.New("db error")}
		s := createTestSynchronizerWithMocks(t, db, &mockSyncPeer{})
		if _, err := s.BlockHeight(t.Context()); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("handles zero block number", func(t *testing.T) {
		db := &mockBlockHeightReader{blockNumber: 0}
		s := createTestSynchronizerWithMocks(t, db, &mockSyncPeer{})
		height, err := s.BlockHeight(t.Context())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if height != 1 {
			t.Errorf("expected 1, got %d", height)
		}
	})
}

// PeerBlockHeight tests

func TestSynchronizer_PeerBlockHeight(t *testing.T) {
	t.Run("returns peer block height", func(t *testing.T) {
		peer := &mockSyncPeer{blockHeightValue: 100}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)
		height, err := s.PeerBlockHeight(t.Context())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if height != 100 {
			t.Errorf("expected 100, got %d", height)
		}
	})

	t.Run("returns error from peer", func(t *testing.T) {
		peer := &mockSyncPeer{blockHeightErr: errors.New("peer error")}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, peer)
		if _, err := s.PeerBlockHeight(t.Context()); err == nil {
			t.Fatal("expected error")
		}
	})
}

// Start position tests

func TestSynchronizer_StartPosition(t *testing.T) {
	t.Run("starts from block 0 when db is empty", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{blockNumber: 0}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait() // in stateSyncing

			if peer.getSubscribeCalls() < 1 {
				t.Error("expected at least one subscribe call")
			}
			cancel()
		})
	})

	t.Run("starts from next block when db has blocks", func(t *testing.T) {
		peer := &mockSyncPeer{subscribeBlocking: true, blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{blockNumber: 10}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			synctest.Wait()

			if peer.getSubscribeCalls() < 1 {
				t.Error("expected at least one subscribe call")
			}
			cancel()
		})
	})
}

// monitorReadiness tests
//
// These call monitorReadiness directly to isolate its behaviour.
// setState sets up the required struct state without running Start().
// monitorCancel is nil in these setups; the nil guard in transition protects against panics.

func TestSynchronizer_MonitorReadiness(t *testing.T) {
	t.Run("transitions to ready when caught up", func(t *testing.T) {
		// blockNumber 9 → localHeight 10; peerHeight 10 → caught up on first tick.
		db := &mockBlockHeightReader{blockNumber: 9}
		peer := &mockSyncPeer{blockHeightValue: 10}
		s := createTestSynchronizerWithMocks(t, db, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			s.setState(stateSyncing)
			go s.monitorReadiness(ctx)

			synctest.Wait()         // monitor blocks on 1 s ticker.
			time.Sleep(time.Second) // advance fake clock 1 s → ticker fires.
			synctest.Wait()         // monitor transitions to ready and exits.

			if s.getState() != stateReady {
				t.Errorf("expected stateReady, got %v", s.getState())
			}
			cancel()
		})
	})

	t.Run("handles peer block height errors gracefully", func(t *testing.T) {
		peer := &mockSyncPeer{blockHeightValue: 10, blockHeightErr: errors.New("peer error")}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{blockNumber: 5}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			s.setState(stateSyncing)
			go s.monitorReadiness(ctx)

			// Let 3 ticks happen; peer error prevents stateReady each time.
			for range 3 {
				time.Sleep(time.Second)
				synctest.Wait()
			}

			if s.getState() == stateReady {
				t.Error("should not transition to ready when peer returns error")
			}
			cancel()
		})
	})

	t.Run("handles db block height errors gracefully", func(t *testing.T) {
		db := &mockBlockHeightReader{blockNumber: 5, err: errors.New("db error")}
		peer := &mockSyncPeer{blockHeightValue: 10}
		s := createTestSynchronizerWithMocks(t, db, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			s.setState(stateSyncing)
			go s.monitorReadiness(ctx)

			for range 3 {
				time.Sleep(time.Second)
				synctest.Wait()
			}

			if s.getState() == stateReady {
				t.Error("should not transition to ready when db returns error")
			}
			cancel()
		})
	})

	t.Run("exits when context is canceled", func(t *testing.T) {
		peer := &mockSyncPeer{blockHeightValue: 1000}
		s := createTestSynchronizerWithMocks(t, &mockBlockHeightReader{blockNumber: 5}, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			s.setState(stateSyncing)
			go s.monitorReadiness(ctx)

			synctest.Wait() // monitor blocks on ticker.
			cancel()        // ctx.Done fires → monitor select picks it immediately.
			synctest.Wait() // monitor exits; if it doesn't, synctest detects deadlock.
		})
	})

	t.Run("monitor goroutine is restarted after reconnect", func(t *testing.T) {
		// First cycle: subscribe error → retrying. Second cycle: syncing → ready.
		db := &mockBlockHeightReader{blockNumber: 9}
		peer := &mockSyncPeer{subscribeErr: errors.New("transient"), blockHeightValue: 10}
		s := createTestSynchronizerWithMocks(t, db, peer)

		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			go func() { _ = s.Start(ctx) }()

			// Cycle 1: subscribe fails → retrying → sleeping 1 s.
			synctest.Wait()
			if s.getState() != stateRetrying {
				t.Errorf("expected stateRetrying, got %v", s.getState())
			}

			peer.setSubscribeError(nil)
			peer.mu.Lock()
			peer.subscribeBlocking = true
			peer.mu.Unlock()

			// Advance past 1 s backoff → cycle 2 begins: syncing, new monitor on ticker.
			time.Sleep(time.Second)
			synctest.Wait()
			if s.getState() != stateSyncing {
				t.Errorf("expected stateSyncing, got %v", s.getState())
			}

			// Advance 1 s → monitor ticks (localHeight 10 >= peerHeight 10) → stateReady.
			time.Sleep(time.Second)
			synctest.Wait()
			if s.getState() != stateReady {
				t.Errorf("expected stateReady, got %v", s.getState())
			}

			cancel()
		})
	})
}

// Helpers

func createTestSynchronizer(t *testing.T) *Synchronizer {
	t.Helper()
	return createTestSynchronizerWithMocks(t, &mockBlockHeightReader{}, &mockSyncPeer{})
}

func createTestSynchronizerWithMocks(t *testing.T, db BlockHeightReader, peer SyncPeer) *Synchronizer {
	t.Helper()
	s, err := NewSynchronizer(db, peer, &mockBlockProcessor{}, sdk.NoOpLogger{})
	if err != nil {
		t.Fatalf("failed to create synchronizer: %v", err)
	}
	return s
}
