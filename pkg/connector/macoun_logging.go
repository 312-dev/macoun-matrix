// macoun-matrix - A Matrix-iMessage puppeting bridge (fork of corten-matrix).
// SPDX-License-Identifier: MPL-2.0
//
// macoun: log levels for failures that matter when the Mac relay is away.
//
// In macoun-matrix, PET refresh needs anisette from the Mac relay. A failed
// refresh is non-fatal and is retried on the next tick, but it should show in
// the default (info) log so a relay outage is visible. Upstream logged these
// at debug. A refresh skipped by the throttle is routine and stays at debug.

package connector

import (
	"errors"

	"github.com/rs/zerolog"
)

// errPetRefreshThrottled marks a PET refresh skipped by petRefreshMinInterval.
var errPetRefreshThrottled = errors.New("PET refresh throttled")

// macounPetRefreshEvent picks the log level for a PET refresh error.
func macounPetRefreshEvent(log zerolog.Logger, err error) *zerolog.Event {
	if errors.Is(err, errPetRefreshThrottled) {
		return log.Debug()
	}
	return log.Warn()
}
