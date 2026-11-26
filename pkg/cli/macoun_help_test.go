// macoun-matrix: test for the user-facing help text.
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestHelpSaysMacounMatrix(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	PrintHelp()
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)
	s := string(out)
	if !strings.Contains(s, "macoun-matrix") || !strings.Contains(s, "Usage:") || strings.Contains(s, "Usage: corten-matrix") {
		t.Fatalf("help text does not name macoun-matrix:\n%s", s)
	}
}
