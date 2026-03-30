/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
)

// BatchingConfig controls batching behavior of the test orderer.
// BlockCutTime is the maximum time to wait before cutting a block (0 = cut immediately).
// MaxTxPerBlock is the maximum number of transactions per block (0 = no limit).
type BatchingConfig struct {
	BlockCutTime  time.Duration
	MaxTxPerBlock int
}

func newTestOrderer(l *ledger, cfg BatchingConfig) *testOrderer {
	o := &testOrderer{
		ledger: l,
		config: cfg,
		inCh:   make(chan *common.Envelope, 64),
	}

	go o.batchingLoop()

	return o
}

// testOrderer receives transaction envelopes and cuts blocks.
// Contrary to a normal orderer, it does MVCC validation in place.
type testOrderer struct {
	ledger *ledger
	inCh   chan *common.Envelope
	config BatchingConfig
}

// Broadcast is the GRPC endpoint that lets users submit transactions.
func (o *testOrderer) Broadcast(stream orderer.AtomicBroadcast_BroadcastServer) error {
	for {
		env, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		o.inCh <- env

		if err := stream.Send(&orderer.BroadcastResponse{
			Status: common.Status_SUCCESS,
		}); err != nil {
			return err
		}
	}
}

func (o *testOrderer) batchingLoop() {
	var (
		batch []*common.Envelope
		timer *time.Timer
	)

	// flush cuts a block
	flush := func() {
		if len(batch) == 0 {
			return
		}

		if err := o.ledger.process(context.Background(), batch); err != nil {
			panic(err)
		}

		batch = nil
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}

	for {
		cfg := o.config

		var timerC <-chan time.Time
		if timer != nil {
			timerC = timer.C
		}

		select {
		case env := <-o.inCh:
			batch = append(batch, env)

			// Start timer on first tx in batch (Fabric semantics: timer and max size
			// both trigger a cut, whichever comes first).
			if len(batch) == 1 && cfg.BlockCutTime > 0 {
				timer = time.NewTimer(cfg.BlockCutTime)
			}

			// Cut immediately if no timer configured, or if max tx count is reached.
			if cfg.BlockCutTime == 0 || (cfg.MaxTxPerBlock > 0 && len(batch) >= cfg.MaxTxPerBlock) {
				flush()
				continue
			}

		case <-timerC:
			flush()
		}
	}
}

// Deliver delivers blocks to peers. We skip that part; we let the orderer do the
// validations and store the blocks on the ledger directly.
func (o *testOrderer) Deliver(orderer.AtomicBroadcast_DeliverServer) error {
	return errors.New("not implemented")
}
