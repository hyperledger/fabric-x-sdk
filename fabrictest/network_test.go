/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabrictest

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-protos-go-apiv2/orderer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestCutBlock_AdvancesHeightAndProducesEmptyBlock(t *testing.T) {
	n, err := Start(t.Context(), "basic", "fabric-x", Config{}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	_, newBlocks := n.ledger.subscribe()
	initialHeight := n.ledger.height()

	if err := n.CutBlock(t.Context()); err != nil {
		t.Fatalf("CutBlock: %v", err)
	}

	if got := n.ledger.height(); got != initialHeight+1 {
		t.Errorf("height after CutBlock: got %d, want %d", got, initialHeight+1)
	}

	select {
	case bl := <-newBlocks:
		if len(bl.Data.Data) != 0 {
			t.Errorf("expected a block with zero transactions, got %d", len(bl.Data.Data))
		}
		if bl.Header.Number != initialHeight {
			t.Errorf("block number: got %d, want %d", bl.Header.Number, initialHeight)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the cut block")
	}
}

// TestCutBlock_ConcurrentWithBroadcast fires CutBlock and real Broadcast traffic
// at the network concurrently (run with -race) to verify CutBlock doesn't race
// with testOrderer.batchingLoop over block numbering or world-state writes.
func TestCutBlock_ConcurrentWithBroadcast(t *testing.T) {
	n, err := Start(t.Context(), "basic", "fabric-x", Config{}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn, err := grpc.NewClient(
		fmt.Sprintf("127.0.0.1:%d", n.OrdererPort),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial orderer: %v", err)
	}
	t.Cleanup(func() { conn.Close() }) //nolint:errcheck
	client := orderer.NewAtomicBroadcastClient(conn)

	const numTx = 20
	const numCuts = 20

	// Build envelopes up front: t.Fatalf is not safe to call from the worker
	// goroutines below.
	envs := make([]*common.Envelope, numTx)
	for i := range envs {
		envs[i] = buildEnvelope(t, "tx"+strconv.Itoa(i), common.HeaderType_MESSAGE, simpleTx("basic"))
	}

	initialHeight := n.ledger.height()

	var wg sync.WaitGroup
	errCh := make(chan error, numTx+numCuts)

	for _, env := range envs {
		wg.Go(func() {
			stream, err := client.Broadcast(t.Context())
			if err != nil {
				errCh <- fmt.Errorf("open broadcast stream: %w", err)
				return
			}
			if err := stream.Send(env); err != nil {
				errCh <- fmt.Errorf("send envelope: %w", err)
				return
			}
			if _, err := stream.Recv(); err != nil {
				errCh <- fmt.Errorf("recv broadcast response: %w", err)
			}
		})
	}

	for range numCuts {
		wg.Go(func() {
			if err := n.CutBlock(t.Context()); err != nil {
				errCh <- fmt.Errorf("CutBlock: %w", err)
			}
		})
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	// A Broadcast ack only confirms the envelope was accepted for ordering, not
	// that its block has been committed yet (matching real Fabric semantics), so
	// poll rather than checking immediately after wg.Wait().
	wantHeight := initialHeight + numTx + numCuts
	deadline := time.Now().Add(5 * time.Second)
	var got uint64
	for time.Now().Before(deadline) {
		got = n.ledger.height()
		if got == wantHeight {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("height after concurrent traffic: got %d, want %d", got, wantHeight)
}
