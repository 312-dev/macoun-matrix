# NOTICE: provenance and licences

macoun-matrix is a fork of **corten-matrix** (Ludvig Rhodin and the
Corten-Matrix Project contributors, originally `github.com/lrhodin/corten-matrix`).

Lineage: mautrix-imessage by Tulir Asokan (2021), ported to the bridgev2
framework as corten-matrix by lrhodin and contributors (notably mackid1993 and
Tae Hagen); macoun-matrix forks the last open-source snapshot of corten-matrix
(the MPL-2.0 archive of 2026-11-01).
It is an independent fork for Linux and is not affiliated with or supported
by the Corten-Matrix Project.

## Sources

| Part | Source | Licence |
|---|---|---|
| Everything not listed below | corten-matrix MPL-2.0 source archive "as it stood on November 1, 2026", `https://corten-matrix.com/assets/corten-matrix-mpl-2.0-2026-11-01.tar.gz`, sha256 `761d3713c925ad8c9cfb695c43e397b3bdb1b29ef75efe71a46892a146206d03`. Imported unchanged as the first commit of this repository. | MPL-2.0 (`LICENSE`). File headers are kept. |
| `pkg/rustpushgo/src/nac_relay.rs` | `RelayOSConfig` from corten-matrix commit `e2adc79d92f4b91fb0ef8bf16439c4411ffbb165` (2026-06-21), adapted; the relay anisette client in the same file is new | AGPL-3.0 (`LICENSES/AGPL-3.0.txt`) |
| `tools/macoun-relay/` | Imported with history (`git subtree`) from the macoun-relay repository: `e176a98` is corten-matrix `e2adc79d92` `tools/nac-relay` and `nac-validation` verbatim; `03a5609` adds `/anisette` (AOSKit), JSON `/health`, a capped log and removes token logging. `launchd/` template is new. | AGPL-3.0 (its own `LICENSE`) |
| `examples/monitoring/` | Written for macoun-matrix | MPL-2.0 |
| `docs/` | Written for macoun-matrix, except `docs/bridge-guide.md`, which is the README from the corten-matrix archive with macoun-matrix's additions | MPL-2.0 |
| `third_party/patches/rustpush/macoun-ids-rereg-and-backoff.patch` | Written for macoun-matrix as a patch against rustpush; it quotes rustpush source as context | SSPL v1 (`LICENSES/SSPL-1.0.txt`) |
| `tools/extract-key/` | corten-matrix `e2adc79d92`, `tools/extract-key/`; header added | AGPL-3.0 |
| `disable_icloud_contacts` (`config.go`, `client.go`, `shared_profile.go`, example config) | The Go patches in corten-matrix `64f9d73`'s Makefile, applied directly | MPL-2.0 |
| `third_party/patches/apple-private-apis/fallible-gsa-token-header-parsing.patch` | corten-matrix commit `64f9d73` (the public tree matching the 1.3.2 release) | MPL-2.0 |
| `scripts/macoun-prepare-rustpush.sh` | The patch set is taken from the `ensure-rustpush-source` target of corten-matrix `64f9d73`'s Makefile, plus one macoun-matrix patch (`pub mod activation`) | MPL-2.0 |
| `pkg/connector/idskeys.go`, `macoun_limiter_store.go` and their tests (written independently), the outbound rich-link fix in `pkg/connector/client.go`, `pkg/connector/outbound_richlink_test.go`, the anisette source selection in `pkg/rustpushgo/src/anisette.rs`, `docker/`, `scripts/macoun-build-linux.sh` | Written for macoun-matrix | MPL-2.0 |

The e2adc79d92 commit predates corten-matrix's move to MPL-2.0 (commit
`f84bc7eb04`, 2026-06-26), so code taken from it stays under AGPL-3.0. Because
`nac_relay.rs` is compiled into `librustpushgo` and linked into the binary,
**a macoun-matrix binary as a whole should be treated as AGPL-3.0** (MPL-2.0 is
compatible with this as a Larger Work, MPL section 3.3).

## Fetched at build time, not in this repository

- **OpenBubbles/rustpush** at the SHA in `third_party/rustpush-upstream.sha`
  (`cb2361c2a750ffa2047c1a64eda44c8fb8364218`), with its submodules
  (apple-private-apis `b8598d2`, clearadi-stub, OpenAbsinthe-Stub, quinn, rtc,
  rustls, rustls-platform-verifier). rustpush is licensed under the
  **Server Side Public License v1** (`LICENSES/SSPL-1.0.txt` is a copy of its
  `LICENSE`); its `LICENSE.exceptions` grants an MIT-style exception to
  OpenBubbles only.
- Go modules in `go.mod` and Rust crates in `pkg/rustpushgo/Cargo.lock`, under
  their own licences.

## Binaries

The bridge is published as source only. A built bridge links MPL-2.0, AGPL-3.0
and SSPL v1 code. Building and running it for your own account is the intended
use. Distributing a bridge binary, or offering it to third parties as a
service, brings SSPL obligations that this combination with AGPL and MPL code
cannot satisfy, so the project does not publish bridge binaries and asks that
you do not either.

The Mac tools are different. `nac-relay` (`tools/macoun-relay/`) and
`extract-key` (`tools/extract-key/`) are AGPL-3.0, have no Go dependencies and
contain no rustpush code. Releases carry universal macOS binaries of the two
(`macoun-mac-tools-<tag>.tar.gz`, built by `.github/workflows/mac-tools.yml`),
with the AGPL-3.0 text and the source commit in the archive.

## corten-matrix parts that are not in its public source

- `pkg/connector/idskeys.go` is a stub in the public source: replaced by an
  independent outbound limiter, see the file header.
- `pkg/connector/hostcmd.go` (`runSelfUpdate`): left as the public stub; there is
  no self-update.
- The Rust `native-nac-rust` NAC and `remote-clearadi` anisette: not built.
  NAC and anisette both come from the Mac relay.
