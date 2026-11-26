# Contributing

macoun-matrix is maintained in spare time by one person. Issues and pull
requests are welcome, but there is no promise of a quick answer.

## Reporting a bug

Open an issue with the bug report template. The most useful reports say what
you did, what you expected, what happened, and include the relevant log lines.

Before posting a log, read it. Bridge logs are anonymised by default, but check
for phone numbers, email addresses, Apple IDs, the relay token, the hardware
key and anything else you would not want public. Never post `session.json`,
`keystore.plist`, the hardware key or the relay's `token` file.

Security problems go through [SECURITY.md](SECURITY.md), not the issue tracker.

## Pull requests

- Keep a change to one thing. A bug fix comes with a test that fails without
  it, or a description of how the bug was reproduced.
- Commit messages follow [Conventional Commits](https://www.conventionalcommits.org):
  `fix: ...`, `feat: ...`, `docs: ...`, lowercase, imperative, no trailing
  period.
- Update the comments and docs that describe the code you change.
- New bridge files are named `macoun_*.go` and carry the MPL-2.0 header, so it
  stays easy to see what differs from the corten-matrix snapshot. The generated
  `pkg/rustpushgo` bindings are not edited by hand, except the hand-written
  `macoun_*.go` files next to them.
- `scripts/macoun-build-linux.sh test` (inside the build image, see
  [docs/setup.md](docs/setup.md)) runs the macoun tests. CI runs the same
  script.
- CI also runs `golangci-lint` ([.golangci.yml](.golangci.yml)) and
  `go mod tidy -diff`. Lint only reports code changed since v0.1.0, so a
  corten-matrix file you touch is checked on your lines, not as a whole.

Nothing in the test suite talks to Apple. If your change affects what is sent
to Apple, say in the pull request how you checked it against a real account.

## Licensing of contributions

By contributing you agree that your contribution is licensed under the licence
of the part you change: MPL-2.0 for the bridge, AGPL-3.0 for
`tools/macoun-relay/`, `tools/extract-key/` and
`pkg/rustpushgo/src/nac_relay.rs`. See [NOTICE.md](NOTICE.md).

Do not contribute code or findings derived from decompiling or disassembling
software whose licence forbids it, and do not paste code from sources that are
not under a compatible open licence.

## Upstream

This is a fork of corten-matrix. Please do not send macoun-matrix bug reports
or questions to the corten-matrix, mautrix or rustpush maintainers. If you find
a bug that clearly exists in rustpush itself, report it there with a
reproduction that does not involve this bridge.
