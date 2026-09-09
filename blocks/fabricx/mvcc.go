/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

func NewMVCCValidator(db blocks.RecordGetter, log sdk.Logger) *blocks.MVCCValidator {
	v := blocks.NewMVCCValidator(db, true, log)
	v.Codes = CodesByStatus()
	return v
}
