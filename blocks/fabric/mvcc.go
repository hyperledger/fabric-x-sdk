/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	sdk "github.com/hyperledger/fabric-x-sdk"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

func NewMVCCValidator(db blocks.RecordGetter, log sdk.Logger) *blocks.MVCCValidator {
	v := blocks.NewMVCCValidator(db, false, log)
	v.Codes = CodesByStatus()
	return v
}
