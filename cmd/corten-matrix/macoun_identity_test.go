// macoun-matrix: test for the user-facing version line.
// SPDX-License-Identifier: MPL-2.0

package main

import (
	"strings"
	"testing"
)

func TestMacounVersionLine(t *testing.T) {
	line := macounVersionLine()
	if !strings.HasPrefix(line, "macoun-matrix ") || !strings.Contains(line, "a fork of corten-matrix") {
		t.Fatalf("version line %q", line)
	}
	// The mxmain name must stay upstream's: it is the database owner and the
	// double-puppet marker of existing installs.
	if m.Name != "corten-matrix" {
		t.Fatalf("BridgeMain.Name changed to %q", m.Name)
	}
}
