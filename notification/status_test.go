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
		{notification.StatusInvalid, "INVALID"},
		{notification.StatusRejected, "REJECTED"},
		{notification.Status(99), "UNKNOWN"}, // unrecognised code falls back to the default label
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
		{notification.StatusInvalid, true},
		{notification.StatusRejected, true},
	}
	for _, c := range cases {
		if got := c.status.IsFinal(); got != c.want {
			t.Errorf("Status(%d).IsFinal() = %v, want %v", c.status, got, c.want)
		}
	}
}
