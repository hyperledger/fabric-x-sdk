/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"github.com/hyperledger/fabric-x-common/api/committerpb"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

func NewMVCCValidator(db blocks.RecordGetter, log sdk.Logger) *blocks.MVCCValidator {
	return blocks.NewMVCCValidator(db,
		byte(committerpb.Status_COMMITTED),
		byte(committerpb.Status_ABORTED_MVCC_CONFLICT),
		byte(committerpb.Status_MALFORMED_BAD_ENVELOPE_PAYLOAD),
		true,
		log,
	)
}
