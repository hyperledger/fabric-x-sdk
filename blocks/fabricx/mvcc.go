/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"github.com/hyperledger/fabric-x-committer/api/protoblocktx"
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

func NewMVCCValidator(db blocks.RecordGetter, log sdk.Logger) *blocks.MVCCValidator {
	return blocks.NewMVCCValidator(db,
		byte(protoblocktx.Status_COMMITTED),
		byte(protoblocktx.Status_ABORTED_MVCC_CONFLICT),
		byte(protoblocktx.Status_MALFORMED_BAD_ENVELOPE_PAYLOAD),
		true,
		log,
	)
}
