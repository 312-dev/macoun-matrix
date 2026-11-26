package cli

import "testing"

func TestLegacyBundleIDDecodes(t *testing.T) {
	want := "com." + "lrhodin" + ".corten-matrix"
	if got := legacyBundleID(); got != want {
		t.Fatalf("legacyBundleID() = %q, want %q", got, want)
	}
	if cortenBundleID == legacyBundleID() {
		t.Fatal("renamed label must differ from the legacy label")
	}
}
