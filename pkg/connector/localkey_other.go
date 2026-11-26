//go:build !darwin

package connector

import "errors"

// localHardwareKey only applies to macOS. On other platforms, log in with the
// External Key flow instead.
func localHardwareKey() (string, error) {
	return "", errors.New("automatic hardware key extraction requires macOS; use the External Key login flow")
}
