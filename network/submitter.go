/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package network

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	sdk "github.com/hyperledger/fabric-x-sdk"
)

// TxPackager converts endorsements to an envelope that can be submitted to a Fabric-like ordering service.
type TxPackager interface {
	PackageTx(sdk.Endorsement) (*common.Envelope, error)
}

// Submitter can be used on both Fabric and Fabric-X.
type Submitter struct {
	orderers []*Orderer
	packager TxPackager
	logger   sdk.Logger
	// waitAfterSubmit is a temporary patch to wait for finality, until we have a proper finality listener. FIXME
	waitAfterSubmit time.Duration
}

// NewSubmitter dials all configured orderers and returns a Submitter.
// Prefer the protocol-specific constructors in network/fabric or network/fabricx over calling this directly.
func NewSubmitter(ctx context.Context, config []OrdererConf, packager TxPackager, waitAfterSubmit time.Duration, logger sdk.Logger) (*Submitter, error) {
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
		orderers:        orderers,
		packager:        packager,
		logger:          logger,
		waitAfterSubmit: waitAfterSubmit,
	}, nil
}

// Submit to each of the registered orderers sequentially, returns an error if more than half errored.
// Uses persistent streams with fire-and-forget sends for better performance.
func (s Submitter) Submit(ctx context.Context, end sdk.Endorsement) error {
	env, err := s.packager.PackageTx(end)
	if err != nil {
		return fmt.Errorf("package proposal: %w", err)
	}

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

	if s.waitAfterSubmit > 0 {
		time.Sleep(s.waitAfterSubmit)
	}

	return nil
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
