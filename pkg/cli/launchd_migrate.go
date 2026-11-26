package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// legacyBundleID returns the launchd label that macOS installs used before the
// service was renamed to cortenBundleID. It is decoded at runtime (ROT13) only
// so the old owner-specific label is not stored verbatim in release binaries.
func legacyBundleID() string {
	return "com." + rot13("yeubqva") + ".corten-matrix"
}

func rot13(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return 'a' + (r-'a'+13)%26
		case r >= 'A' && r <= 'Z':
			return 'A' + (r-'A'+13)%26
		}
		return r
	}, s)
}

// migrateLegacyLaunchAgent moves a macOS install from the legacy LaunchAgent
// label to cortenBundleID. It runs before every management command (including
// the `restart` that `update` performs with the freshly installed binary), so an
// existing install switches over on its first update without user action.
//
// It is a no-op on Linux, when no legacy plist exists, and when it has already
// run. The legacy job is stopped before the renamed one is loaded so two
// bridges never run against the same config.
func migrateLegacyLaunchAgent() {
	if runtime.GOOS != "darwin" {
		return
	}
	home := os.Getenv("HOME")
	if home == "" {
		return
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	oldLabel := legacyBundleID()
	oldPlist := filepath.Join(dir, oldLabel+".plist")
	body, err := os.ReadFile(oldPlist)
	if err != nil {
		return
	}
	newPlist := filepath.Join(dir, cortenBundleID+".plist")
	renamed := strings.ReplaceAll(string(body),
		"<string>"+oldLabel+"</string>", "<string>"+cortenBundleID+"</string>")
	if err := os.WriteFile(newPlist, []byte(renamed), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not migrate the LaunchAgent to %s: %v\n", newPlist, err)
		return
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain+"/"+oldLabel).Run()
	_ = exec.Command("launchctl", "unload", "-w", oldPlist).Run()
	if err := os.Remove(oldPlist); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove the old LaunchAgent %s: %v\n", oldPlist, err)
	}
	if err := exec.Command("launchctl", "load", "-w", newPlist).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: LaunchAgent migrated but could not be loaded: %v\n", err)
		return
	}
	fmt.Printf("Migrated the background service to the %s LaunchAgent.\n", cortenBundleID)
}
