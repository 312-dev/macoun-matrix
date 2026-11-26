# Security policy

## Reporting a vulnerability

Please report security problems privately, through GitHub's
[private vulnerability reporting](https://github.com/312-dev/macoun-matrix/security/advisories/new)
for this repository. Do not open a public issue for them.

Include what is affected (the bridge, `tools/macoun-relay`, `tools/extract-key`
or the build scripts), how to reproduce it, and what an attacker gains. Do not
include real credentials, hardware keys, relay tokens or message content.

This is a spare-time project with one maintainer. Expect an acknowledgement
within about a week. Fixes land on `main`; there are no maintained release
branches, so the supported version is always the latest tag.

## What is in scope

- The bridge leaking message content, credentials or contact data to logs, to
  Matrix rooms that should not see it, or to third parties.
- The relay answering without a valid token, logging its token, or listening
  somewhere other than the address it was given.
- The bridge sending its relay token to a server that fails the certificate
  pin.
- Anything in the build scripts that fetches or runs code other than what the
  pinned commits and lock files name.

## What is not in scope

- Apple's services and private frameworks themselves.
- Bugs in rustpush, mautrix-go or the homeserver; report those to their
  projects.
- A relay deliberately exposed to an untrusted network. It is meant to be bound
  to a private interface only; see [docs/requirements.md](docs/requirements.md).

## Handling secrets

The hardware key, the relay token, `session.json` and `keystore.plist` give
access to your Apple account's iMessage identity. Keep them out of issues,
logs you share and version control.
