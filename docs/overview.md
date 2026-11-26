# macoun-matrix overview

## What it is

macoun-matrix is a Matrix to iMessage puppeting bridge for Linux. It is a fork
of [corten-matrix](https://corten-matrix.com) (Ludvig Rhodin and the
Corten-Matrix Project contributors), built only from source that carries an
open licence.

Upstream corten-matrix releases contain closed parts: the Linux NAC and
anisette code, an outbound IDS precheck, and a self-updater. The public source
has stubs in their place and cannot register a Linux bridge with Apple. The
fork replaces those parts with open code and a Mac that does the Apple-specific
work.

The fork exists so that every line in the binary has a licence you can read and
keep using. No closed code and no PolyForm-licensed code is included, linked,
loaded at run time or required.

## Licences

| Part | Licence |
|---|---|
| The bridge (everything from the corten-matrix 2026-11-01 MPL archive, plus macoun's own Go code) | MPL-2.0 |
| The relay client in the bridge (`pkg/rustpushgo/src/nac_relay.rs`) | AGPL-3.0 (from corten-matrix e2adc79d92) |
| The Mac relay (`tools/macoun-relay/`) and `tools/extract-key/` | AGPL-3.0 (from corten-matrix e2adc79d92, plus macoun changes) |
| OpenBubbles rustpush, fetched at build time at a pinned commit | Server Side Public License v1 |

Because AGPL code is linked into the binary, treat a built binary as AGPL-3.0.
SSPL rustpush makes it unsuitable for distribution or for offering to other
people as a service: build it for your own account and do not hand out
binaries. [NOTICE.md](../NOTICE.md) has the file-level map and the source
commits.

## Architecture

```
 Matrix homeserver  <-->  macoun-matrix (Linux)  <-->  Apple (APNs, IDS, CloudKit)
                                  |
                                  |  HTTPS, bearer token, pinned certificate
                                  |  (over Tailscale or another private network)
                                  v
                           macoun-relay (a Mac)
                           POST /validation-data   NAC via AAAbsintheContext
                           GET  /anisette          anisette via AOSKit
                           GET  /health
```

- **NAC validation data** proves to Apple's IDS that a registration comes from
  a real Mac. The bridge asks the relay for it whenever it registers.
- **Anisette headers** (`X-Apple-I-MD`, `X-Apple-I-MD-M` and friends) are
  one-time codes Apple requires on Apple ID and iCloud requests. The bridge asks
  the relay each time it needs fresh ones (they are cached for 60 seconds).
- The hardware key the bridge logs in with is extracted from the same Mac, so
  the device identity, the NAC data and the anisette machine all match. Apple
  has rejected mismatched setups (for example Android-generated anisette with a
  Mac identity) since about 2026-10-12.
- Both are pluggable: any service that speaks the
  [provider contract](provider-contract.md) can serve either role.

## When the Mac must be reachable

Sending and receiving messages, attachments, reactions and receipts never touch
the relay. These do:

| Event | How often | If the Mac is unreachable |
|---|---|---|
| Bridge start: CloudKit bootstrap | Every start, plus a few passes in the next 20 minutes | Bootstrap retries every minute. On an install that has synced before, macoun opens the message gate after 3 failed attempts, so messages flow and new rooms are created; bootstrap keeps retrying in the background. A fresh install waits. |
| Apple ID token (PET) refresh | About 7 times a day | Logged at warn and retried on the next tick. iCloud features degrade after about a day without a refresh. |
| StatusKit (presence) pass | About every 4 hours | Presence updates pause and retry. |
| MobileMe delegate refresh | Weekly | CloudKit and contacts calls fail until the next successful refresh. |
| IDS re-registration | Every 45 days (the identity certificate lifetime) | macoun re-registers 7 days before expiry, retries with backoff capped at 30 minutes, and logs each failure at warn. Sending stops while registration is failing; receiving stops if the certificate actually expires. |
| Apple ID login | Only when you log in | Login cannot complete. |

So the Mac needs to be up most of the time, but a nap of hours, or a reboot,
does not break messaging.
