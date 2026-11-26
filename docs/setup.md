# Setting up macoun-matrix from scratch

Read [overview.md](overview.md) and [requirements.md](requirements.md) first.
Placeholders used below:

| Placeholder | Meaning |
|---|---|
| `<relay-dir>` | Where the relay lives on the Mac, for example `/Users/you/macoun-relay` |
| `<mac-ip>` | The Mac's private address the bridge can reach, for example its Tailscale IP |
| `<data-dir>` | The bridge's data directory on Linux |

## 1. Prepare the Mac

Run as an admin user on the Mac:

```bash
# No system sleep (display sleep is fine); restart after power loss; wake on LAN
sudo pmset -a sleep 0 displaysleep 10
sudo pmset -a autorestart 1 womp 1
# No automatic macOS installs (a new build can break the private frameworks),
# but keep security responses and system data files
sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate AutomaticallyInstallMacOSUpdates -bool false
sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate CriticalUpdateInstall -bool true
sudo defaults write /Library/Preferences/com.apple.SoftwareUpdate ConfigDataInstall -bool true
# Check
pmset -g | grep -E '^ (sleep|displaysleep|autorestart|womp)'
defaults read /Library/Preferences/com.apple.SoftwareUpdate
```

- In System Settings > General > Software Update > Automatic Updates, turn off
  "Install macOS updates" and leave "Install Security Responses and system
  files" on.
- Turn on auto-login for the relay's user if the relay must survive unattended
  reboots (this needs FileVault off; see requirements.md for the trade-off).
- In the Tailscale admin console, disable key expiry for the Mac.
- Sign in to iCloud with the Apple ID the bridge will use.

## 2. Build and run the relay on the Mac

Either download the release build (macOS 13 and later, Intel or Apple
Silicon):

```bash
mkdir -p <relay-dir>/bin <relay-dir>/logs <relay-dir>/state
cd <relay-dir>
T=<tag>   # for example v0.1.0
curl -fsSLO "https://github.com/312-dev/macoun-matrix/releases/download/$T/macoun-mac-tools-$T.tar.gz"
curl -fsSL "https://github.com/312-dev/macoun-matrix/releases/download/$T/sha256sums-mac-tools.txt" | shasum -a 256 -c
tar -xzf "macoun-mac-tools-$T.tar.gz"
cp "macoun-mac-tools-$T/nac-relay" bin/
```

Download with `curl` as above rather than a browser: a browser marks the file
as quarantined and Gatekeeper refuses to run an unsigned binary. The archive
also holds `extract-key` (step 3) and the LaunchAgent template.

Or build it from source:

```bash
git clone https://github.com/312-dev/macoun-matrix.git
mkdir -p <relay-dir>/bin <relay-dir>/logs <relay-dir>/state
cd macoun-matrix/tools/macoun-relay
go build -o <relay-dir>/bin/nac-relay ./tools/nac-relay
```

Install it as a LaunchAgent from the template. **Never run `nac-relay
--setup`**: that upstream option installs its own .app and LaunchAgent bound to
all interfaces.

```bash
# The template is in the release archive, or at
# tools/macoun-relay/launchd/ in a source checkout.
sed -e 's#@RELAY_DIR@#<relay-dir>#g' -e 's#@LISTEN_IP@#<mac-ip>#g' \
  <path-to>/dev.macoun.relay.plist.template \
  > ~/Library/LaunchAgents/dev.macoun.relay.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.macoun.relay.plist
launchctl print gui/$(id -u)/dev.macoun.relay | grep -E 'state|pid'
```

The agent sets `HOME=<relay-dir>/state`, so on first start the relay writes
its files to `<relay-dir>/state/Library/Application Support/nac-relay/`:
`cert.pem` and `key.pem` (self-signed TLS), `token` (the bearer token, mode
0600) and `relay-info.json` (read by extract-key). The token is never logged
and is not passed on the command line. Logs go to `<relay-dir>/logs/relay.log`,
capped at 5 MB with 3 old files kept.

The relay binds only to `<mac-ip>`. If that address is not up yet at boot, the
relay exits and launchd retries every 30 seconds.

### Values the bridge needs

```bash
D="<relay-dir>/state/Library/Application Support/nac-relay"
# MACOUN_NAC_RELAY_CERT_FP: SHA-256 of the DER certificate
openssl x509 -in "$D/cert.pem" -outform der | shasum -a 256 | cut -d' ' -f1
# curl/monitoring pin: SHA-256 of the public key, base64
openssl x509 -in "$D/cert.pem" -pubkey -noout | openssl pkey -pubin -outform der \
  | openssl dgst -sha256 -binary | base64
# MACOUN_NAC_RELAY_TOKEN: copy it to your secret store without printing it, e.g.
pbcopy < "$D/token"
```

Check from the Linux host (`/health` needs no token):

```bash
curl -sk --pinnedpubkey 'sha256//<public key pin>' https://<mac-ip>:5001/health
```

To rotate the token, stop the agent, delete `token` and `relay-info.json`,
start the agent, and update the bridge's `MACOUN_NAC_RELAY_TOKEN`. Deleting
`cert.pem`/`key.pem` changes both pins.

## 3. Extract the hardware key

Only needed for a new login. The key must come from the same Mac that runs the
relay, so the identity matches the NAC data and anisette.

```bash
# From the release archive of step 2:
HOME=<relay-dir>/state <relay-dir>/macoun-mac-tools-<tag>/extract-key
# Or from source:
cd macoun-matrix/tools/extract-key
./build.sh      # or: go build -o extract-key .
HOME=<relay-dir>/state ./extract-key
```

The relay fields are optional in the key: the bridge's `MACOUN_NAC_RELAY_*`
variables override them. (The tool's printed hints refer to upstream paths
such as `tools/nac-relay/main.go`; ignore them.) Store the key output as a
secret; it identifies your Mac.

## 4. Build the bridge on Linux

```bash
git clone https://github.com/312-dev/macoun-matrix.git
git clone https://github.com/OpenBubbles/rustpush.git rustpush
git -C rustpush checkout "$(cat macoun-matrix/third_party/rustpush-upstream.sha)"
git -C rustpush -c url.https://github.com/.insteadOf=git@github.com: \
  submodule update --init --recursive
docker build -t macoun-build:local macoun-matrix/docker -f macoun-matrix/docker/Dockerfile.build
docker run --rm --cpus 3 --memory 6g \
  -v "$PWD/macoun-matrix:/src" -v "$PWD/rustpush:/rustpush:ro" \
  macoun-build:local bash -c 'git config --global --add safe.directory "*"; scripts/macoun-build-linux.sh build'
```

The result is `macoun-matrix/macoun-matrix`. `scripts/macoun-build-linux.sh
test` runs the macoun tests. The rustpush checkout is copied and patched, never
modified in place; the script refuses a checkout with local changes.

## 5. Configure the bridge

```bash
./macoun-matrix -e -c config.yaml          # writes the example config
# edit homeserver.address, homeserver.domain, appservice.address, database, ...
./macoun-matrix -g -c config.yaml -r registration.yaml
```

Register `registration.yaml` with your homeserver. Environment for the bridge
process:

| Variable | Value |
|---|---|
| `MACOUN_NAC_RELAY_URL` | `https://<mac-ip>:5001` |
| `MACOUN_NAC_RELAY_TOKEN` | the relay token (from a secret store) |
| `MACOUN_NAC_RELAY_CERT_FP` | the DER certificate SHA-256 from step 2 |
| `XDG_DATA_HOME` | parent of the bridge state directory; state lives in `$XDG_DATA_HOME/corten-matrix/` (the upstream name is kept for compatibility) |
| `RUST_LOG` | suggested: `warn,rustpush=warn,rustpush::ids::identity_manager=info,rustpushgo=info` |
| `MACOUN_OUTBOUND_LIMIT` | `off` disables the outbound limiter (not recommended) |

In `config.yaml`, under the network section: with an external CardDAV
server configured (`carddav:`, for example Google), iCloud CardDAV is not used
for contact names anyway. `disable_icloud_contacts: true` also stops the
periodic remote refresh of shared profiles (Name and Photo Sharing); cached
profiles are still shown. Leave it `false` to keep that refresh.

The outbound limiter keeps its state in a new table, `macoun_outbound_limiter`,
encrypted with `macoun-limiter.key` in the state directory (created on first
use).

Two more sets of tables make starts fast (see "Startup" in
[overview.md](overview.md)); both are created on first use:

- `macoun_attachment_index` and `macoun_attachment_index_state`: the CloudKit
  attachment zone (record names, file metadata and the per-attachment Ford
  keys), each row encrypted with `macoun-attachment-index.key` in the state
  directory. If the key file is lost the index is rebuilt from CloudKit on the
  next start.
- `macoun_recycle_bin_record` and `macoun_recycle_bin_zone`: a copy of Apple's
  recycle bin (message GUIDs, chat identifiers and participants, timestamps;
  no message text) with the CloudKit position it was read up to.

Anisette comes from the same relay. There is no public anisette fallback: with
no source configured, anisette calls fail with an error. See
[provider-contract.md](provider-contract.md) for using another provider.

Start with `./macoun-matrix -c config.yaml`, then log in from Matrix through
the bridge bot with the hardware key (External Key flow), the Apple ID
password and a 2FA code.

## 6. Migrating an existing corten-matrix 1.3.x login

If the existing login's hardware key came from the Mac that now runs the relay,
no new login is needed. Work on a **copy** first.

1. Stop the old bridge. Copy the database (with any `-wal`/`-shm` files) and
   the whole `corten-matrix/` state directory. `keystore.plist` must come
   along: without it the bridge clears the login and forces a re-login.
2. Back up `corten-matrix/anisette/state.plist` separately. macoun does not
   use that file; keeping it makes a rollback clean.
3. Do not re-extract the key and do not edit it. Point the bridge at the relay
   with the `MACOUN_NAC_RELAY_*` variables instead; the database stays
   byte-identical, so rollback is just starting the old binary on the old copy.
4. Have the relay up before the first start: if the stored registration needs
   refreshing, the bridge registers at once.
5. Expect Apple to see the Mac's own anisette machine on the next Apple ID
   token refresh. A 2FA prompt or iCloud features needing a re-login are
   possible; messaging does not depend on it.
6. Check the start log for `macoun-matrix starting` and `Hardware key:
   validation data from NAC relay`. If the second is missing, the relay
   settings were not picked up.
7. macoun adds the `macoun_outbound_limiter`, `macoun_attachment_index*` and
   `macoun_recycle_bin_*` tables and the key files `macoun-limiter.key` and
   `macoun-attachment-index.key`; corten-matrix 1.3.x does not use any of
   them, so a rollback is unaffected. It leaves session.json's `ids_key_cache`
   untouched. macoun also writes `macoun_rehydrate_empty:<portal>` and
   `macoun_local_delete:<portal>` rows into the existing `cloud_maintenance`
   table, which corten-matrix 1.3.x does not read.

## 7. Monitoring

### In Matrix

The bridge reports an outage of the Mac relay (or whichever provider serves
validation data and anisette) as a bridge state. With `bridge_status_notices:
errors` (or `all`) in `config.yaml`, the bridge bot posts it in your management
room:

- when a validation-data or anisette call has failed after its retries: one
  notice, `CONNECTED` with error `macoun-provider-unreachable`, saying which
  kind of call failed and that messaging continues. Further failed calls in
  the same outage post nothing.
- when a later call succeeds: one `CONNECTED` notice saying the relay answers
  again.

The login stays `CONNECTED` throughout, because sending and receiving do not
depend on the relay; clients do not show the account as disconnected. The
bridge only talks to the relay when it needs something (see "When the Mac must
be reachable" in [overview.md](overview.md)), so an outage is noticed at the
next such call, and so is its end. The check runs every 30 seconds.

With `bridge_status_notices: none` nothing is posted; the log lines below
still appear.

### Outside the bridge

A bridge that is down cannot report anything, so an external watchdog is still
worth having. `examples/monitoring/` has a probe script and systemd timers:

- every 5 minutes, `GET /health` with the public key pinned; alert after 15
  minutes of failure, repeat every 6 hours, note recovery and macOS build
  changes;
- daily, days until the IDS identity certificate in `session.json` expires
  (warn under 7 days, high under 2, urgent if also unreachable) and days until
  the Mac's Tailscale key expires.

Set `MACOUN_RELAY_URL`, `MACOUN_RELAY_PIN`, `MACOUN_SESSION_JSON` and an ntfy
URL file (`NTFY_FILE`). Also watch the bridge log for warn lines starting
`NAC relay:`, `anisette:`, `IDS register failed` and `PET refresh`.

## 8. Upgrades

- **Bridge:** rebuild from the repository. Changing the rustpush pin
  (`third_party/rustpush-upstream.sha`) means re-checking every patch in
  `scripts/macoun-prepare-rustpush.sh`; the script stops on any that no longer
  applies.
- **Relay:** rebuild with `go build`, then `launchctl kickstart -k
  gui/$(id -u)/dev.macoun.relay`.
- **macOS on the relay Mac:** install 15.x security updates by hand, then check
  `/health` and make one real `/validation-data` call. Do not move to macOS 26
  or 27 without testing.

## 9. Troubleshooting

| Symptom | Check |
|---|---|
| `/health` times out | Is the Mac awake and on the private network? `launchctl print gui/$(id -u)/dev.macoun.relay`; `<relay-dir>/logs/relay.log` |
| `/health` 503, `framework_ok: false` | A macOS update changed `AAAbsintheContext`; roll back or wait for a relay fix |
| `/health` 503, anisette error | AOSKit failed; is the user logged in to the GUI session and to iCloud? |
| Bridge log `certificate fingerprint ... does not match` | The relay certificate was regenerated; update `MACOUN_NAC_RELAY_CERT_FP` |
| Bridge log `NAC relay error` with 401 | Wrong or rotated token |
| Bridge log `no anisette source configured` | No `MACOUN_NAC_RELAY_URL` (and no relay in the key) |
| Sends fail after an outage | Registration retries every 30 minutes at most; a bridge restart retries at once |
