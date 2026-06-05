/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"context"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	"github.com/hyperledger/fabric-x-common/protoutil"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/network"
	"github.com/hyperledger/fabric-x-sdk/notification"
)

// NewTxPackager returns a TxPackager that assembles classic Fabric transaction envelopes.
func NewTxPackager(s sdk.Signer) TxPackager {
	return TxPackager{
		signer: s,
	}
}

// TxPackager assembles a signed Fabric transaction envelope from an endorsement.
type TxPackager struct {
	signer sdk.Signer
}

// PackageTx combines the proposal and endorser responses into a signed Fabric envelope.
func (p TxPackager) PackageTx(end sdk.Endorsement) (*common.Envelope, error) {
	return protoutil.CreateSignedTx(end.Proposal, p.signer, end.Responses...)
}

// NewSubmitter is a convenience constructor that wires together a Fabric
// TxPackager and a Submitter for classic Fabric orderers. finality is optional
// and may be nil; classic Fabric has no FinalityProvider implementation yet, so
// only fire-and-forget Submit is currently supported.
func NewSubmitter(ctx context.Context, orderers []network.OrdererConf, s sdk.Signer, finality notification.FinalityProvider, logger sdk.Logger) (*network.Submitter, error) {
	return network.NewSubmitter(ctx, orderers, NewTxPackager(s), finality, logger)
}
