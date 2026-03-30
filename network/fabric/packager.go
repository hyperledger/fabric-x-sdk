/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"time"

	"github.com/hyperledger/fabric-protos-go-apiv2/common"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/network"
	"github.com/hyperledger/fabric/protoutil"
)

func NewTxPackager(s sdk.Signer) TxPackager {
	return TxPackager{
		signer: s,
	}
}

type TxPackager struct {
	signer sdk.Signer
}

func (p TxPackager) PackageTx(end sdk.Endorsement) (*common.Envelope, error) {
	return protoutil.CreateSignedTx(end.Proposal, p.signer, end.Responses...)
}

func NewSubmitter(orderers []network.OrdererConf, s sdk.Signer, waitAfterSubmit time.Duration, logger sdk.Logger) (*network.FabricSubmitter, error) {
	return network.NewSubmitter(orderers, NewTxPackager(s), waitAfterSubmit, logger)
}
