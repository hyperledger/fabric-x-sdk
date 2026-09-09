/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package fabricx

import (
	"testing"

	"github.com/hyperledger/fabric-x-common/api/committerpb"
	"github.com/hyperledger/fabric-x-sdk/blocks"
)

// TestCodesByStatus_RoundTrips pins CodesByStatus's contract: every code it returns must
// map back onto the Status it's keyed by via StatusFromCommitterStatus. This is what
// deriving the reverse mapping from the forward one, instead of hand-picking codes,
// guarantees — the two can't silently drift apart.
func TestCodesByStatus_RoundTrips(t *testing.T) {
	codes := CodesByStatus()
	for _, status := range []blocks.Status{blocks.StatusCommitted, blocks.StatusMVCCConflict, blocks.StatusUnknown} {
		code, ok := codes[status]
		if !ok {
			t.Errorf("CodesByStatus: no code for %v", status)
			continue
		}
		got, _, _ := StatusFromCommitterStatus(committerpb.Status(code))
		if got != status {
			t.Errorf("CodesByStatus[%v] = %d, but StatusFromCommitterStatus(%d) = %v", status, code, code, got)
		}
	}
}
