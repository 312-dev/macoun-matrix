# macoun-matrix

A Matrix to iMessage puppeting bridge for Linux, built on
[mautrix-go bridgev2](https://github.com/mautrix/go) and
[rustpush](https://github.com/OpenBubbles/rustpush). The bridge talks to
Apple's servers directly. A Mac you own runs a small relay that supplies the
two things Apple only accepts from a real Mac: registration (NAC) validation
data and anisette headers.

macoun-matrix is a fork of [corten-matrix](https://corten-matrix.com), built
from its last open-source snapshot (the MPL-2.0 source archive of 2026-11-01).
See [Lineage](#lineage).

This is an unofficial project. It is not affiliated with, endorsed by or
supported by Apple Inc. or the Corten-Matrix Project.

## Features

| Feature | Matrix to iMessage | iMessage to Matrix |
|---|---|---|
| Text, with formatting | yes | yes |
| Images, video, audio, files, voice messages | yes | yes |
| Replies | yes | yes |
| Reactions (tapbacks) | yes | yes |
| Bubble and screen effects ([docs/effects.md](docs/effects.md)) | yes, text only | yes |
| Edits | yes | yes |
| Unsend / redaction | yes | yes |
| Read receipts | yes | yes |
| Typing notifications | yes | yes |
| Direct chats | yes | yes |
| Group chats: messages | yes | yes |
| Group chats: create, rename, photo, add, remove, leave | yes (iMessage groups) | yes |
| SMS/MMS/RCS through an iPhone (Text Message Forwarding) | yes | yes |
| History backfill from iCloud (CloudKit) | n/a | yes, optional |
| Contact names (iCloud or an external CardDAV server) | n/a | yes |
| FaceTime call notices | n/a | yes, notice only |
| Focus / Do Not Disturb status of contacts | n/a | yes |
| iCloud Shared Albums | n/a | yes |
| Link previews | yes | yes |

Not supported: FaceTime calls themselves, threads, polls, managing carrier
(SMS/MMS/RCS) groups, and running the bridge without a Mac.

Group management from Matrix is new and lightly tested. Treat this as alpha
software and keep a backup of the bridge database.

## Requirements

- **A Linux host** (amd64) for the bridge, with Docker to build it and a Matrix
  homeserver you control (developed against Synapse) that accepts an
  appservice registration.
- **A Mac that stays on**, signed in to iCloud with the Apple ID the bridge
  uses, running `tools/macoun-relay`. macOS 13 to 15 are the supported range;
  macOS 26 is untested and **macOS 27 is known not to work** for anisette.
  Intel is verified, Apple Silicon is expected to work.
- **A private network between the two**, such as Tailscale or WireGuard. The
  relay must never listen on the LAN or the internet.

The Mac is needed at login, at registration (every 45 days), at Apple ID token
refreshes (a few times a day) and at bridge start. Sending and receiving
messages do not pass through it, so a reboot or a few hours of downtime on the
Mac does not interrupt messaging. Details:
[docs/requirements.md](docs/requirements.md) and
[docs/overview.md](docs/overview.md#when-the-mac-must-be-reachable).

## Quick start

The full walk-through is [docs/setup.md](docs/setup.md). In short:

1. **On the Mac:** build `tools/macoun-relay`, install it as a LaunchAgent
   bound to the Mac's private address, and note the relay token and
   certificate fingerprint.
2. **On the Mac:** run `tools/extract-key` once to get the hardware key the
   bridge logs in with.
3. **On Linux:** build the bridge.

   ```bash
   git clone https://github.com/312-dev/macoun-matrix.git
   git clone https://github.com/OpenBubbles/rustpush.git
   git -C rustpush checkout "$(cat macoun-matrix/third_party/rustpush-upstream.sha)"
   git -C rustpush -c url.https://github.com/.insteadOf=git@github.com: \
     submodule update --init --recursive
   docker build -t macoun-build:local macoun-matrix/docker -f macoun-matrix/docker/Dockerfile.build
   docker run --rm --memory 6g \
     -v "$PWD/macoun-matrix:/src" -v "$PWD/rustpush:/rustpush:ro" \
     macoun-build:local bash -c 'git config --global --add safe.directory "*"; scripts/macoun-build-linux.sh build'
   ```

4. **On Linux:** write the config and the appservice registration, and
   register it with your homeserver.

   ```bash
   ./macoun-matrix -e -c config.yaml
   ./macoun-matrix -g -c config.yaml -r registration.yaml
   ```

5. Start the bridge with the relay settings in its environment
   (`MACOUN_NAC_RELAY_URL`, `MACOUN_NAC_RELAY_TOKEN`,
   `MACOUN_NAC_RELAY_CERT_FP`), then log in from Matrix through the bridge bot
   with the hardware key, your Apple ID password and a 2FA code.

There are no binary releases; see [Licence](#licence) for why.

## Configuration

- `config.yaml` is a standard bridgev2 config. `./macoun-matrix -e` writes the
  example, and [pkg/imconfig/example-config.yaml](pkg/imconfig/example-config.yaml)
  is the reference for the iMessage options.
- The relay connection and a few macoun-specific switches are environment
  variables, listed in [docs/setup.md](docs/setup.md#5-configure-the-bridge)
  and [docs/provider-contract.md](docs/provider-contract.md).
- The bridge name inside the database, the Go module path, the
  `cmd/corten-matrix` package and the `corten-matrix` state directory keep
  their upstream names, so an existing corten-matrix database and data
  directory work unchanged.

## Documentation

| Read | For |
|---|---|
| [docs/overview.md](docs/overview.md) | What it is, architecture, when the Mac must be reachable, what happens at startup |
| [docs/requirements.md](docs/requirements.md) | Mac, Linux host and network requirements |
| [docs/setup.md](docs/setup.md) | Step by step: relay, hardware key, build, config, migrating a corten-matrix 1.3.x login, monitoring, upgrades, troubleshooting |
| [docs/provider-contract.md](docs/provider-contract.md) | The HTTP contract, to plug in your own validation-data or anisette provider |
| [docs/bridge-guide.md](docs/bridge-guide.md) | Bridge commands, groups, FaceTime notices, media conversion, privacy and the config options |
| [docs/effects.md](docs/effects.md) | The content key and Apple effect ids for bubble and screen effects, both directions |
| [Wiki](https://github.com/312-dev/macoun-matrix/wiki) | The same material arranged by task, plus a FAQ |
| [NOTICE.md](NOTICE.md) | File-level provenance and licences |

## What differs from corten-matrix

- NAC validation data and anisette come from HTTP providers, by default the
  relay on your Mac. There is no public anisette fallback.
- An outbound send limiter (`pkg/connector/idskeys.go`) that fails a refused
  send visibly in the room.
- Startup holds back only the chats that need the CloudKit pass, keeps an
  incremental copy of the recycle bin and an encrypted index of the attachment
  zone, so later starts make a handful of CloudKit requests.
- A relay outage and its recovery are reported as bridge states, and messaging
  continues while the Mac is briefly away.
- iMessage groups can be created and managed from Matrix.
- `delete-portal` and `delete-all-portals` record the deletion, so a deleted
  room is not created again at the next start.
- No self-update.

## Discussion and support

Use [GitHub Discussions](https://github.com/312-dev/macoun-matrix/discussions)
for questions and [Issues](https://github.com/312-dev/macoun-matrix/issues)
for bugs. There is no Matrix room for this project yet. Please do not take
macoun-matrix questions to the corten-matrix or mautrix rooms; their
maintainers do not support this fork.

Security reports: see [SECURITY.md](SECURITY.md).

## Lineage

macoun-matrix descends from Tulir Asokan's mautrix-imessage (2021). It was
ported to the bridgev2 framework as corten-matrix by Ludvig Rhodin and
contributors (notably mackid1993 and Tae Hagen). macoun-matrix forks the last
open-source snapshot of corten-matrix, its MPL-2.0 source archive of
2026-11-01, imported unchanged as the first commit of this repository. The
parts of corten-matrix that are not in that archive are replaced here with
open code and a Mac you own. Almost all of the bridge is their work; thank you.

## Licence

- The bridge is [MPL-2.0](LICENSE).
- `tools/macoun-relay/`, `tools/extract-key/` and
  `pkg/rustpushgo/src/nac_relay.rs` are AGPL-3.0
  ([LICENSES/AGPL-3.0.txt](LICENSES/AGPL-3.0.txt)).
- [rustpush](https://github.com/OpenBubbles/rustpush) is fetched at build time
  and is under the Server Side Public License v1
  ([LICENSES/SSPL-1.0.txt](LICENSES/SSPL-1.0.txt)), as are the patches against
  it in `third_party/patches/rustpush/`.

[NOTICE.md](NOTICE.md) has the file-level map. Because a built binary links
AGPL and SSPL code together, this project publishes source only: build the
bridge for your own account, and do not redistribute binaries or run it as a
service for other people.

## Disclaimer

iMessage, FaceTime, iCloud, Mac and macOS are trademarks of Apple Inc. This
project is not affiliated with or endorsed by Apple. It uses Apple's services
in ways Apple does not document or support; using it may be against the terms
of your Apple account, and Apple can break it or restrict an account at any
time. Use it only with your own Apple ID, at your own risk.
