#!/usr/bin/env bash
# macoun-matrix: build the Linux binary. Run inside docker/Dockerfile.build with
# the repo at /src and an OpenBubbles/rustpush checkout at the pinned SHA at
# /rustpush (read-only is fine).
#
#   scripts/macoun-build-linux.sh [build|test]
#
# Rust features: no default (the default `cleanroom-registration` needs closed
# crates), `hardware-key` for the hardware-key OSConfig and the NAC relay, and
# `anisette-remote-v3` for the open anisette v3 client.
set -euo pipefail
cd "$(dirname "$0")/.."
MODE=${1:-build}
FEATURES="--no-default-features --features hardware-key,anisette-remote-v3"
VERSION=${MACOUN_VERSION:-0.1.0}
COMMIT=${MACOUN_COMMIT:-unknown}

scripts/macoun-prepare-rustpush.sh /rustpush

(cd pkg/rustpushgo && cargo build --release --locked $FEATURES)
cp pkg/rustpushgo/target/release/librustpushgo.a .
# flate2 in librustpushgo uses the system zlib; the generated bindings' linux
# LDFLAGS do not name it.
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -lz"

if [ "$MODE" = test ]; then
  (cd pkg/rustpushgo && cargo test --release --locked $FEATURES --lib nac_relay)
  go test ./pkg/connector/ -run 'TestOutbound' -count=1 -v
  exit 0
fi

go build -trimpath \
  -ldflags "-X main.Tag=$VERSION -X main.Commit=$COMMIT -X main.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o macoun-matrix ./cmd/corten-matrix/
sha256sum macoun-matrix
