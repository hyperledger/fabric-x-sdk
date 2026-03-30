/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

func NewMVCCValidator(db blocks.RecordGetter, log sdk.Logger) *blocks.MVCCValidator {
	return blocks.NewMVCCValidator(db,
		byte(peer.TxValidationCode_VALID),
		byte(peer.TxValidationCode_MVCC_READ_CONFLICT),
		byte(peer.TxValidationCode_INVALID_WRITESET),
		false,
		log,
	)
}
