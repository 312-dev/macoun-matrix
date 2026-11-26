//go:build darwin

package connector

import "errors"

// localHardwareKey reads this Mac's own hardware identifiers for iMessage
// registration. The real implementation ships only in the official prebuilt
// macOS release; building from source on macOS is no longer supported (see the
// README). This stub keeps the public tree compiling.
func localHardwareKey() (string, error) {
	return "", errors.New("building corten-matrix from source on macOS is not supported; download the prebuilt macOS release")
}
