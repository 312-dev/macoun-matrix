# NOTICE: provenance and licences

macoun-matrix is a fork of **corten-matrix** (Ludvig Rhodin and the
Corten-Matrix Project contributors, originally `github.com/lrhodin/corten-matrix`).
It is a private, self-built variant for Linux and is not affiliated with or
supported by the Corten-Matrix Project.

## Sources

| Part | Source | Licence |
|---|---|---|
| Everything not listed below | corten-matrix MPL-2.0 source archive "as it stood on November 1, 2026", `https://corten-matrix.com/assets/corten-matrix-mpl-2.0-2026-11-01.tar.gz`, sha256 `761d3713c925ad8c9cfb695c43e397b3bdb1b29ef75efe71a46892a146206d03`. Imported unchanged as the first commit of this repository. | MPL-2.0 (`LICENSE`). File headers are kept. |
| `pkg/rustpushgo/src/nac_relay.rs` | `RelayOSConfig` from corten-matrix commit `e2adc79d92f4b91fb0ef8bf16439c4411ffbb165` (2026-06-21), adapted | AGPL-3.0 (`LICENSES/AGPL-3.0.txt`) |
| `tools/nac-relay/` | corten-matrix `e2adc79d92`, `tools/nac-relay/{main,auth}.go`; header and `darwin` build constraint added | AGPL-3.0 |
| `tools/extract-key/` | corten-matrix `e2adc79d92`, `tools/extract-key/`; header added | AGPL-3.0 |
| `third_party/patches/apple-private-apis/fallible-gsa-token-header-parsing.patch` | corten-matrix commit `64f9d73` (the public tree matching the 1.3.2 release) | MPL-2.0 |
| `scripts/macoun-prepare-rustpush.sh` | The patch set is taken from the `ensure-rustpush-source` target of corten-matrix `64f9d73`'s Makefile, plus one macoun-matrix patch (`pub mod activation`) | MPL-2.0 |
| `pkg/connector/idskeys.go`, its test, the outbound rich-link fix in `pkg/connector/client.go`, `pkg/connector/outbound_richlink_test.go`, `MACOUN_ANISETTE_URL` in `pkg/rustpushgo/src/anisette.rs`, `docker/`, `scripts/macoun-build-linux.sh` | Written for macoun-matrix | MPL-2.0 |

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
  **Server Side Public License v1**; its `LICENSE.exceptions` grants an MIT-style
  exception to OpenBubbles only. Private self-hosting for one's own account is
  the intended use here. Distributing a binary, or offering it to third parties
  as a service, brings SSPL obligations that this combination with AGPL/MPL code
  cannot satisfy. Do not distribute macoun-matrix binaries.
- Go modules in `go.mod` and Rust crates in `pkg/rustpushgo/Cargo.lock`, under
  their own licences.

## Closed corten-matrix parts that are not here

- `pkg/connector/idskeys.go` (a stub in the public source): replaced by an
  independent outbound limiter, see the file header.
- `pkg/connector/hostcmd.go` (`runSelfUpdate`): left as the public stub; there is
  no self-update.
- The Rust `native-nac-rust` NAC and `remote-clearadi` anisette: not built.
  NAC comes from the Mac relay, anisette from an anisette v3 server.
