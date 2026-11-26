// Added 2026-11-25 (macoun-relay). AGPL v3 or later (see LICENSE).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCappedLogRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.log")
	l, err := openCappedLog(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40 bytes, so 2 lines per file
	for i := 0; i < 20; i++ {
		fmt.Fprint(l, line)
	}
	for _, p := range []string{path, path + ".1", path + ".2", path + ".3"} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatalf("%s missing: %v", p, err)
		}
		if st.Size() > 100 {
			t.Fatalf("%s is %d bytes, cap is 100", p, st.Size())
		}
	}
	if _, err := os.Stat(path + ".4"); err == nil {
		t.Fatalf("%s.4 exists, keep is 3", path)
	}
}
