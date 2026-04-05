/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	sdk "github.com/hyperledger/fabric-x-sdk"
)

// Synchronizer connects to a committing peer to maintain a local copy of the world state.
type Synchronizer struct {
	db        BlockHeightReader
	peer      SyncPeer
	log       sdk.Logger
	processor BlockProcessor

	syncing     atomic.Bool
	lastSyncErr atomic.Pointer[error]
}

type BlockHeightReader interface {
	BlockNumber(context.Context) (uint64, error)
}

type SyncPeer interface {
	SubscribeBlocks(context.Context, uint64, BlockProcessor) error
	BlockHeight(context.Context) (uint64, error)
	Close() error
}

// NewSynchronizer creates a new synchronizer.
func NewSynchronizer(db BlockHeightReader, peer SyncPeer, processor BlockProcessor, logger sdk.Logger) (*Synchronizer, error) {
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

func (s *Synchronizer) Start(ctx context.Context) error {
	currentBackoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return nil
		}

		// Read from DB on every (re)connect so we resume from the latest processed block.
		// BlockNumber returns 0 in two cases: the DB is fresh (no blocks yet), or block 0
		// was the last block processed. We always start from 0 in both cases. This is safe
		// to do because there are never any state changing transactions in block 0.
		lastBlock, err := s.db.BlockNumber(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.log.Warnf("failed to read block height from DB: %v — retrying in %s", err, currentBackoff)
			if err := sleepCtx(ctx, currentBackoff); err != nil {
				return nil
			}
			currentBackoff = min(currentBackoff*2, maxBackoff)
			continue
		}

		var start uint64
		if lastBlock > 0 {
			start = lastBlock + 1
		}

		s.log.Infof("starting synchronization from block %d...", start)
		s.syncing.Store(true)
		err = s.peer.SubscribeBlocks(ctx, start, s.processor)
		s.syncing.Store(false)
		if err != nil {
			s.lastSyncErr.Store(&err)
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

// BlockHeight returns the block height, i.e. the index of the next block to be processed.
func (s *Synchronizer) BlockHeight(ctx context.Context) (uint64, error) {
	lpb, err := s.db.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	return lpb + 1, nil
}

// PeerBlockHeight returns the peer's current blockchain height.
func (s *Synchronizer) PeerBlockHeight(ctx context.Context) (uint64, error) {
	return s.peer.BlockHeight(ctx)
}

// WaitUntilSynced blocks until the synchronizer has processed all blocks up to the peer's current height.
// Returns an error if the context is canceled or times out.
// WaitUntilSynced currently only works for Fabric peers due to the implementation of PeerBlockHeight.
func (s *Synchronizer) WaitUntilSynced(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	backoff := time.Second
	const maxBackoff = 32 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			peerHeight, err := s.PeerBlockHeight(ctx)
			if err != nil {
				s.log.Warnf("error getting peer height: %v — retrying in %s", err, backoff)
				if err := sleepCtx(ctx, backoff); err != nil {
					return ctx.Err()
				}

				backoff *= 2
				if backoff > maxBackoff {
					return fmt.Errorf("repeated errors getting peer blockheight: %w", err)
				}
				continue
			}
			backoff = time.Second
			localHeight, err := s.BlockHeight(ctx)
			if err != nil {
				return fmt.Errorf("get local block height: %w", err)
			}
			if uint64(localHeight) >= peerHeight {
				s.log.Infof("synchronized blocks (%d/%d)", localHeight, peerHeight)
				return nil
			}
			s.log.Debugf("synchronizing blocks (%d/%d)", localHeight, peerHeight)
		}
	}
}

// Healthy returns nil if the synchronizer currently has an active deliver stream
// with the peer. It returns the last deliver error if the stream has failed, or
// a "not yet connected" error if Start has not yet established its first connection.
func (s *Synchronizer) Healthy() error {
	if s.syncing.Load() {
		return nil
	}
	if ep := s.lastSyncErr.Load(); ep != nil {
		return *ep
	}
	return errors.New("not yet connected")
}

// Close releases the underlying peer connection.
// It should be called after Start has returned.
func (s *Synchronizer) Close() error {
	return s.peer.Close()
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
