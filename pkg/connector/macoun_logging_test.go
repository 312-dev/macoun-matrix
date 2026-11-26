// macoun-matrix: test for PET refresh log levels.
// SPDX-License-Identifier: MPL-2.0

package connector

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestMacounPetRefreshEventLevel(t *testing.T) {
	var buf bytes.Buffer
	log := zerolog.New(&buf).Level(zerolog.InfoLevel)

	macounPetRefreshEvent(log, fmt.Errorf("%w: last refresh 1m ago", errPetRefreshThrottled)).Msg("throttled")
	if buf.Len() != 0 {
		t.Fatalf("throttled refresh logged at info or above: %s", buf.String())
	}
	macounPetRefreshEvent(log, errors.New("anisette from the Mac relay failed")).Msg("failed")
	if !strings.Contains(buf.String(), `"level":"warn"`) {
		t.Fatalf("real PET refresh failure not logged at warn: %q", buf.String())
	}
}
