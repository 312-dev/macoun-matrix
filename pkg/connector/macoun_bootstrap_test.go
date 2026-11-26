// macoun-matrix: tests for the CloudKit bootstrap fail-open rule.
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"context"
	"testing"

	"github.com/rs/zerolog"
)

func TestMacounFailOpenDecision(t *testing.T) {
	cases := []struct {
		failures int
		prior    bool
		want     bool
	}{
		{1, true, false},
		{macounBootstrapFailOpenAfter - 1, true, false},
		{macounBootstrapFailOpenAfter, true, true},
		{macounBootstrapFailOpenAfter + 5, true, true},
		// A fresh install never fails open: no tombstones are known yet.
		{macounBootstrapFailOpenAfter + 5, false, false},
	}
	for _, tc := range cases {
		if got := macounFailOpenDecision(tc.failures, tc.prior); got != tc.want {
			t.Errorf("failures=%d prior=%v: got %v, want %v", tc.failures, tc.prior, got, tc.want)
		}
	}
}

func TestMacounFailOpenNeedsStore(t *testing.T) {
	c := &IMClient{}
	if c.macounShouldFailOpen(context.Background(), zerolog.Nop(), macounBootstrapFailOpenAfter+1) {
		t.Fatal("failed open without a cloud store to prove an earlier sync")
	}
}
