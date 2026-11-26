// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: the user-facing name. mxmain's BridgeMain.Name stays "corten-matrix"
// on purpose: mautrix uses it as the database owner ("megabridge/<Name>"), as
// the double-puppet marker on events it sends, and in the HTTP user agent, so
// changing it would make the bridge refuse an existing corten-matrix database
// and mis-detect its own double-puppeted events. The state directory
// ($XDG_DATA_HOME/corten-matrix), the Go module path and this package keep
// their upstream names for the same compatibility reason. Only what a person
// reads (version line, help, startup banner) says macoun-matrix.

package main

import (
	"fmt"
	"runtime"
)

const (
	productName  = "macoun-matrix"
	upstreamName = "corten-matrix"
)

// macounVersionLine is printed for -v / --version in place of mxmain's line.
// --version-json is left to mxmain, so tools that parse it see the upstream
// structure unchanged.
func macounVersionLine() string {
	return fmt.Sprintf("%s %s (commit %s, built %s with %s; a fork of %s)",
		productName, Tag, Commit, BuildTime, runtime.Version(), upstreamName)
}
