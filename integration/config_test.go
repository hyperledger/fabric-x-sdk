/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package integration

import (
	"testing"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-sdk/blocks"
	"google.golang.org/protobuf/proto"
)

func TestSynchronizer_ConfigGenesis(t *testing.T) {
	for _, networkType := range []string{"fabric", "fabric-x"} {
		t.Run(networkType, func(t *testing.T) {
			s := newWithTestBackend(t, networkType)
			deadline := time.NewTimer(10 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			var genesis blocks.Block
			found := false
			// Readiness can precede delivery when a fresh store's height already
			// matches a genesis-only peer. Wait for the handler to receive block 0.
			for !found {
				s.capture.mu.Lock()
				for _, block := range s.capture.blocks {
					if block.Number == 0 {
						genesis, found = block, true
						break
					}
				}
				s.capture.mu.Unlock()
				if !found {
					select {
					case <-ticker.C:
					case <-deadline.C:
						t.Fatal("handler did not receive genesis within 10s")
					}
				}
			}
			if genesis.ConfigTransaction == nil {
				t.Fatal("handler received genesis without ConfigTransaction")
			}
			if len(genesis.Transactions) != 0 {
				t.Errorf("genesis contains %d application transactions", len(genesis.Transactions))
			}
			config := &common.Config{}
			if err := proto.Unmarshal(genesis.ConfigTransaction.Config, config); err != nil {
				t.Fatal(err)
			}
			if genesis.ConfigTransaction.Sequence != 0 || config.Sequence != 0 || config.ChannelGroup == nil {
				t.Errorf("unexpected genesis config: transaction=%+v config=%v", genesis.ConfigTransaction, config)
			}
			if number, err := s.localDB.BlockNumber(t.Context()); err != nil || number != 0 {
				t.Errorf("BlockNumber = (%d, %v), want (0, nil)", number, err)
			}
		})
	}
}
