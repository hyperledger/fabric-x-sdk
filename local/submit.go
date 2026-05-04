/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package local can be used to mock a fabric backend for testing. Instead of submitting
// to an orderer, it can insert read/write sets directly in a local database. Some MVCC
// checks are provided, but should not be trusted to be 1:1 compatible with a real network.
package local

import (
	"context"
	"fmt"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

type VersionedDB interface {
	BlockNumber(ctx context.Context) (uint64, error)
	Handle(ctx context.Context, b blocks.Block) error
	Get(namespace, key string, lastBlock uint64) (*blocks.WriteRecord, error)
}

// LocalSubmitter directly stores the writes in the shared database.
// It can be used with any ledger type by providing the relevant TxPackager
// and TxParser, dependent on the ledger type in the endorsement.
type LocalSubmitter struct {
	sharedState       VersionedDB
	packager          TxPackager
	parser            TxParser
	channel           string
	namespace         string
	monotonicVersions bool // if true, MVCC read validation uses WriteRecord.Version instead of BlockNum/TxNum
}

// TxPackager converts endorsements to an envelope that can be submitted to a Fabric-like ordering service.
type TxPackager interface {
	PackageTx(sdk.Endorsement) (*common.Envelope, error)
}

// TxParser extracts read-write sets from transaction envelopes.
type TxParser interface {
	ParseTx(env *common.Envelope) (*blocks.Transaction, error)
}

func NewLocalSubmitter(sharedState VersionedDB, channel, namespace string, packager TxPackager, parser TxParser, monotonicVersions bool) *LocalSubmitter {
	c := &LocalSubmitter{
		sharedState:       sharedState,
		channel:           channel,
		namespace:         namespace,
		packager:          packager,
		parser:            parser,
		monotonicVersions: monotonicVersions,
	}

	return c
}

func (s LocalSubmitter) Submit(ctx context.Context, end sdk.Endorsement) error {
	// package the transaction in the format that the backend ledger expects...
	env, err := s.packager.PackageTx(end)
	if err != nil {
		return fmt.Errorf("package proposal: %w", err)
	}

	// ...and immediately unpackage again to extract the read/write sets.
	tx, err := s.parser.ParseTx(env)
	if err != nil {
		return fmt.Errorf("unpackage proposal: %w", err)
	}
	tx.Valid = true

	blockNum, err := s.sharedState.BlockNumber(ctx)
	if err != nil {
		return err
	}

	var rws blocks.ReadWriteSet
	for _, n := range tx.NsRWS {
		if n.Namespace == s.namespace {
			rws = n.RWS
			break
		}
	}

	if err := validateReads(s.sharedState, s.namespace, blockNum, rws.Reads, s.monotonicVersions); err != nil {
		return err
	}

	return s.sharedState.Handle(ctx, blocks.Block{
		Number:       blockNum + 1,
		Transactions: []blocks.Transaction{*tx},
	})
}

func (s *LocalSubmitter) Close() error {
	return nil
}

func validateReads(st VersionedDB, ns string, blockNum uint64, reads []blocks.KVRead, fabricX bool) error {
	for _, r := range reads {
		rec, err := st.Get(ns, r.Key, blockNum)
		if err != nil {
			return fmt.Errorf("failed to get state for key %q: %v", r.Key, err)
		}

		if rec == nil && r.Version == nil {
			continue // both nil, valid
		}

		if rec == nil && r.Version != nil {
			return fmt.Errorf("RWS INVALID for key %q: expected version, got nil", r.Key)
		}

		if rec != nil && r.Version == nil {
			if fabricX {
				// In Fabric-X, proto version=0 is dropped by the parser (we expect version > 0),
				// resulting in nil. Nil means no version constraint — treat as blind write.
				continue
			}
			return fmt.Errorf("RWS INVALID for key %q: expected nil version, got a record", r.Key)
		}

		// rec != nil && r.Version != nil: compare according to ledger type.
		if fabricX {
			if rec.Version != r.Version.BlockNum {
				return fmt.Errorf(
					"RWS INVALID for key %q: read version=%d, blockchain version=%d",
					r.Key, r.Version.BlockNum, rec.Version,
				)
			}
		} else {
			if rec.BlockNum != r.Version.BlockNum || rec.TxNum != r.Version.TxNum {
				return fmt.Errorf(
					"RWS INVALID for key %q: read version=%d:%d, blockchain version=%d:%d",
					r.Key, r.Version.BlockNum, r.Version.TxNum,
					rec.BlockNum, rec.TxNum,
				)
			}
		}
	}
	return nil
}
