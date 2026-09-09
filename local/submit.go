/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

// package local can be used to mock a fabric backend for testing. Instead of submitting
// to an orderer, it can insert read/write sets directly in a local database. MVCC checks
// use the same comparison rules as blocks.MVCCValidator, but other aspects of a real
// network (ordering, endorsement policies, cryptographic verification) are not modeled.
package local

import (
	"context"
	"fmt"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// VersionedDB is the storage interface required by LocalSubmitter for committing blocks.
// state.VersionedDB satisfies this interface.
type VersionedDB interface {
	BlockNumber(ctx context.Context) (uint64, error)
	Handle(ctx context.Context, b blocks.Block) error
}

// LocalSubmitter directly stores the writes in the shared database.
// It can be used with any ledger type by providing the relevant TxPackager
// and TxParser, dependent on the ledger type in the endorsement.
type LocalSubmitter struct {
	sharedState       VersionedDB
	recordGetter      blocks.RecordGetter // reads sharedState's latest value for MVCC validation
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

// NewLocalSubmitter creates a LocalSubmitter. recordGetter is used to validate MVCC reads
// against sharedState's latest committed value; it's usually sharedState's own
// CurrentRecordGetter().
// Set monotonicVersions=true for Fabric-X MVCC semantics (per-key version counter) and
// false for classic Fabric (blockNum/txNum pairs).
func NewLocalSubmitter(sharedState VersionedDB, recordGetter blocks.RecordGetter, channel, namespace string, packager TxPackager, parser TxParser, monotonicVersions bool) *LocalSubmitter {
	c := &LocalSubmitter{
		sharedState:       sharedState,
		recordGetter:      recordGetter,
		channel:           channel,
		namespace:         namespace,
		packager:          packager,
		parser:            parser,
		monotonicVersions: monotonicVersions,
	}

	return c
}

// Submit packages the endorsement, validates MVCC reads against the shared state,
// and writes the resulting block directly to the database without going through an orderer.
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
	tx.SetStatus(blocks.StatusCommitted, 0, "")

	blockNum, err := s.sharedState.BlockNumber(ctx)
	if err != nil {
		return err
	}

	if err := s.checkMVCC(tx); err != nil {
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

// checkMVCC validates this submitter's namespace's reads in tx against the shared
// state's latest values, using the same comparison rules as the real committer
// (blocks.MVCCValidator). Other namespaces in tx, if any, are not this shared
// state's concern and are left unchecked.
func (s LocalSubmitter) checkMVCC(tx *blocks.Transaction) error {
	var rws blocks.ReadWriteSet
	for _, n := range tx.NsRWS {
		if n.Namespace == s.namespace {
			rws = n.RWS
			break
		}
	}

	// A fresh validator per call avoids sharing MVCCValidator's pending-writes
	// state across concurrent Submit calls; it holds nothing else worth reusing.
	validator := blocks.NewMVCCValidator(s.recordGetter, s.monotonicVersions, sdk.NoOpLogger{})

	block := &blocks.Block{
		Transactions: []blocks.Transaction{{
			NsRWS: []blocks.NsReadWriteSet{{Namespace: s.namespace, RWS: rws}},
		}},
	}
	if _, err := validator.Validate(block); err != nil {
		return fmt.Errorf("failed to get state for read validation: %w", err)
	}
	if !block.Transactions[0].Valid() {
		return fmt.Errorf("RWS INVALID for tx %q in namespace %q: %s", tx.ID, s.namespace, block.Transactions[0].Reason)
	}
	return nil
}
