/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/notification"
)

// TxPackager converts endorsements to an envelope that can be submitted to a Fabric-like ordering service.
type TxPackager interface {
	PackageTx(sdk.Endorsement) (*common.Envelope, error)
}

// Submitter submits transactions to classic Fabric or Fabric-X.
type Submitter struct {
	orderers []*Orderer
	packager TxPackager
	logger   sdk.Logger
	finality notification.FinalityProvider // optional; nil = fire-and-forget
}

// NewSubmitter dials the configured orderers and returns a Submitter.
//
// finality is optional and may be nil. It is only used by SubmitAndWait, which
// requires it; Submit never waits regardless. When non-nil, the provider's Start
// loop must be running. It is fixed at construction so the Submitter is safe for
// concurrent use.
//
// Prefer the protocol-specific constructors in network/fabric or network/fabricx
// over calling this directly.
func NewSubmitter(ctx context.Context, config []OrdererConf, packager TxPackager, finality notification.FinalityProvider, logger sdk.Logger) (*Submitter, error) {
	if len(config) == 0 {
		return nil, errors.New("no orderers configured")
	}

	orderers := make([]*Orderer, len(config))
	for i, cfg := range config {
		if o, err := NewOrderer(ctx, cfg); err != nil {
			return nil, err
		} else {
			orderers[i] = o
		}
	}

	return &Submitter{
		orderers: orderers,
		packager: packager,
		logger:   logger,
		finality: finality,
	}, nil
}

// Submit packages and broadcasts the endorsement to the configured orderers and
// returns as soon as a majority have accepted it. It is fire-and-forget: it does
// not wait for the transaction to be committed. To wait for finality, use
// SubmitAndWait (which requires a FinalityProvider).
func (s *Submitter) Submit(ctx context.Context, end sdk.Endorsement) error {
	env, err := s.packager.PackageTx(end)
	if err != nil {
		return fmt.Errorf("package proposal: %w", err)
	}
	return s.broadcastEnv(ctx, env)
}

// SubmitAndWait broadcasts the endorsement and waits for the transaction to
// reach finality via the configured FinalityProvider. It returns the full
// TxStatusEvent so callers can inspect the commit status; a non-committed status
// is not an error (inspect event.Valid()). Use Submit if you do not want to wait.
//
// Register is called before broadcasting to ensure the notification is not
// missed even if the transaction commits very quickly.
//
// Timeouts: if the provider's subscription times out (for a FinalityListener,
// when the sidecar's per-request timeout elapses before the transaction is
// validated) it releases the wait with a StatusUnknown event and a nil error.
// This is not a final outcome (event.Status.IsFinal() is false): the transaction
// may still commit afterwards, but the wait has ended and that later outcome is
// not delivered. Check event.Status (not only event.Valid()) if you need to
// distinguish a genuine finality result from a released-on-timeout wait.
//
// Returns an error if no FinalityProvider is configured or if broadcasting fails.
func (s *Submitter) SubmitAndWait(ctx context.Context, end sdk.Endorsement) (notification.TxStatusEvent, error) {
	if s.finality == nil {
		return notification.TxStatusEvent{}, errors.New("no finality provider configured; pass one to NewSubmitter")
	}

	txID, err := txIDFromProposalHeader(end.Proposal.Header)
	if err != nil {
		return notification.TxStatusEvent{}, fmt.Errorf("extract txID: %w", err)
	}

	// Register BEFORE packaging and broadcasting so we cannot miss a fast commit.
	ch, err := s.finality.Register(ctx, txID)
	if err != nil {
		return notification.TxStatusEvent{}, fmt.Errorf("register finality: %w", err)
	}

	env, err := s.packager.PackageTx(end)
	if err != nil {
		s.finality.Unregister(txID)
		return notification.TxStatusEvent{}, fmt.Errorf("package tx: %w", err)
	}
	if err := s.broadcastEnv(ctx, env); err != nil {
		s.finality.Unregister(txID)
		return notification.TxStatusEvent{}, err
	}

	select {
	case event := <-ch:
		return event, nil
	case <-ctx.Done():
		s.finality.Unregister(txID)
		return notification.TxStatusEvent{}, ctx.Err()
	}
}

// broadcastEnv sends env to all orderers and returns an error if a majority fail.
func (s *Submitter) broadcastEnv(ctx context.Context, env *common.Envelope) error {
	var errs int
	for i, o := range s.orderers {
		if err := o.Broadcast(ctx, env); err != nil {
			s.logger.Warnf("orderer %d (%s): broadcast failed: %v", i, o.addr, err)
			errs++
		}
	}
	if errs*2 > len(s.orderers) {
		return errors.New("error broadcasting")
	}
	return nil
}

// txIDFromProposalHeader extracts the transaction ID from the serialised
// proposal header bytes (peer.Proposal.Header).
func txIDFromProposalHeader(proposalHeader []byte) (string, error) {
	header, err := protoutil.UnmarshalHeader(proposalHeader)
	if err != nil {
		return "", fmt.Errorf("unmarshal header: %w", err)
	}
	chdr, err := protoutil.UnmarshalChannelHeader(header.ChannelHeader)
	if err != nil {
		return "", fmt.Errorf("unmarshal channel header: %w", err)
	}
	return chdr.TxId, nil
}

// Close closes the connections to all orderers.
func (s *Submitter) Close() error {
	var errs []error
	for _, o := range s.orderers {
		if err := o.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
