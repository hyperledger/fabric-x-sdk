/*
Copyright IBM Corp. All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package notification_test

import (
	"testing"

	"github.com/hyperledger/fabric-x-sdk/notification"
)

func TestStatus_String(t *testing.T) {
	cases := []struct {
		status notification.Status
		want   string
	}{
		{notification.StatusUnknown, "UNKNOWN"},
		{notification.StatusCommitted, "COMMITTED"},
		{notification.StatusInvalidSignature, "INVALID_SIGNATURE"},
		{notification.StatusMVCCConflict, "MVCC_CONFLICT"},
		{notification.StatusDuplicateTxID, "DUPLICATE_TX_ID"},
		{notification.StatusMalformed, "MALFORMED"},
		{notification.StatusUnrecognized, "UNRECOGNIZED"},
		{notification.Status(99), "UNKNOWN"}, // arbitrary raw value with no constant falls back to the default label
	}
	for _, c := range cases {
		if got := c.status.String(); got != c.want {
			t.Errorf("Status(%d).String() = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestStatus_IsFinal(t *testing.T) {
	cases := []struct {
		status notification.Status
		want   bool
	}{
		{notification.StatusUnknown, false},
		{notification.StatusCommitted, true},
		{notification.StatusInvalidSignature, true},
		{notification.StatusMVCCConflict, true},
		{notification.StatusDuplicateTxID, true},
		{notification.StatusMalformed, true},
		{notification.StatusUnrecognized, true},
	}
	for _, c := range cases {
		if got := c.status.IsFinal(); got != c.want {
			t.Errorf("Status(%d).IsFinal() = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestStatus_Valid(t *testing.T) {
	cases := []struct {
		status notification.Status
		want   bool
	}{
		{notification.StatusUnknown, false},
		{notification.StatusCommitted, true},
		{notification.StatusInvalidSignature, false},
		{notification.StatusMVCCConflict, false},
		{notification.StatusDuplicateTxID, false},
		{notification.StatusMalformed, false},
		{notification.StatusUnrecognized, false},
	}
	for _, c := range cases {
		if got := c.status.Valid(); got != c.want {
			t.Errorf("Status(%d).Valid() = %v, want %v", c.status, got, c.want)
		}
	}
}
