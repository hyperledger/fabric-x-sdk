/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabric

import (
	"testing"

	"github.com/hyperledger/fabric-protos-go-apiv2/peer"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// TestCodesByStatus_RoundTrips pins CodesByStatus's contract: every code it returns must
// map back onto the Status it's keyed by via StatusFromValidationCode.
func TestCodesByStatus_RoundTrips(t *testing.T) {
	codes := CodesByStatus()
	for _, status := range []blocks.Status{blocks.StatusCommitted, blocks.StatusMVCCConflict, blocks.StatusUnknown} {
		code, ok := codes[status]
		if !ok {
			t.Errorf("CodesByStatus: no code for %v", status)
			continue
		}
		got, _, _ := StatusFromValidationCode(peer.TxValidationCode(code))
		if got != status {
			t.Errorf("CodesByStatus[%v] = %d, but StatusFromValidationCode(%d) = %v", status, code, code, got)
		}
	}
}
