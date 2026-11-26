#!/usr/bin/env bash
# macoun-matrix: prepare third_party/rustpush-upstream for a Linux build.
#
# Usage: scripts/macoun-prepare-rustpush.sh <rustpush checkout>
#
# <rustpush checkout> is an OpenBubbles/rustpush clone with submodules. It is
# copied (never modified) to third_party/rustpush-upstream, checked against the
# pin in third_party/rustpush-upstream.sha, then patched.
#
# Every patch is fail-closed, as in corten-matrix 64f9d73's Makefile, from
# which most of these are taken: a missing target file or an anchor that no
# longer matches stops the script; a patch already satisfied is skipped.
set -euo pipefail

SRC=${1:?usage: $0 <rustpush checkout>}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
PIN=$(tr -d '[:space:]' < third_party/rustpush-upstream.sha)
RP=third_party/rustpush-upstream
APA=$RP/third_party/apple-private-apis

head=$(git -C "$SRC" rev-parse HEAD)
[ "$head" = "$PIN" ] || { echo "error: $SRC is at $head, pin is $PIN" >&2; exit 1; }
if [ -n "$(git -C "$SRC" status --porcelain --ignore-submodules=none)" ]; then
  echo "error: $SRC has local changes" >&2; exit 1
fi

# Skip when the tree was already prepared from the same inputs, so cargo's
# incremental build is not invalidated by fresh mtimes on every run.
stamp=$( { echo "$PIN"; cat "$0"; find third_party/patches rustpush/open-absinthe -type f -print0 | sort -z | xargs -0 cat; } | sha256sum | cut -d' ' -f1)
if [ -f "$RP/.macoun-prepared" ] && [ "$(cat "$RP/.macoun-prepared")" = "$stamp" ]; then
  echo "rustpush already prepared at $RP ($PIN)"
  exit 0
fi

rm -rf "$RP"
cp -a "$SRC" "$RP"

# FairPlay cert stubs, as upstream CI does.
mkdir -p "$RP/certs/fairplay"
for name in 4056631661436364584235346952193 4056631661436364584235346952194 \
            4056631661436364584235346952195 4056631661436364584235346952196 \
            4056631661436364584235346952197 4056631661436364584235346952198 \
            4056631661436364584235346952199 4056631661436364584235346952200 \
            4056631661436364584235346952201 4056631661436364584235346952208; do
  cp "$RP/certs/legacy-fairplay/fairplay.crt" "$RP/certs/fairplay/$name.crt"
  cp "$RP/certs/legacy-fairplay/fairplay.pem" "$RP/certs/fairplay/$name.pem"
done

# The bridge's open-absinthe (HardwareConfig, _enc helpers) replaces the stub.
rm -rf "$RP/open-absinthe"
cp -a rustpush/open-absinthe "$RP/open-absinthe"

rp_patch() { # label file perl-expr verify-ere
  local label=$1 file=$2 expr=$3 verify=$4
  [ -f "$file" ] || { echo "error: patch '$label': missing $file" >&2; return 1; }
  if grep -qE "$verify" "$file"; then echo "  patch $label (already satisfied)"; return 0; fi
  perl -i -pe "$expr" "$file"
  grep -qE "$verify" "$file" || { echo "error: patch '$label' did not apply to $file" >&2; return 1; }
  echo "  patch $label"
}

rp_apply() { # label root patch-file verify-file verify-ere
  local label=$1 root=$2 pf=$ROOT/$3 target=$2/$4 verify=$5
  [ -f "$pf" ] && [ -f "$target" ] || { echo "error: patch '$label': missing file" >&2; return 1; }
  if git -C "$root" apply --reverse --unidiff-zero --whitespace=fix --check "$pf" >/dev/null 2>&1; then
    grep -qE "$verify" "$target" || { echo "error: patch '$label' present without marker" >&2; return 1; }
    echo "  patch $label (already satisfied)"; return 0
  fi
  git -C "$root" apply --unidiff-zero --whitespace=fix "$pf"
  grep -qE "$verify" "$target" || { echo "error: patch '$label' applied without marker" >&2; return 1; }
  echo "  patch $label"
}

# rustpushgo's [features] names omnisette prefer-* features the public
# omnisette lacks; cargo validates the whole table, so define them as no-ops.
rp_patch "omnisette prefer-* no-op features" "$APA/omnisette/Cargo.toml" \
  's/^(remote-clearadi = .*)$/$1\nprefer-aoskit = []\nprefer-remote-anisette-v3 = []/' \
  '^prefer-aoskit'
# CloudKit user messages arrive as msgType 0; upstream decodes only 1..=2.
rp_patch "decode msgType=0 as a message" "$RP/src/imessage/cloud_messages.rs" \
  's/^                1\.\.=2 => CloudMessageType::Message \{/                0..=2 => CloudMessageType::Message {/' \
  '^                0\.\.=2 => CloudMessageType::Message \{'
rp_patch "pub mod ids" "$RP/src/lib.rs" 's/^mod ids;$/pub mod ids;/' '^pub mod ids;'
# macoun-matrix: RelayOSConfig implements OSConfig outside rustpush and has to
# name ActivationInfo in build_activation_info's signature.
rp_patch "pub mod activation" "$RP/src/lib.rs" 's/^mod activation;$/pub mod activation;/' '^pub mod activation;'
rp_patch "FetchedToken.token pub" "$APA/icloud-auth/src/client.rs" \
  's/^    token: String,$/    pub token: String,/' '^    pub token: String,'
rp_patch "FetchedToken.expiration pub" "$APA/icloud-auth/src/client.rs" \
  's/^    expiration: SystemTime,$/    pub expiration: SystemTime,/' '^    pub expiration: SystemTime,'
rp_apply "fallible GSA token header parsing" "$APA" \
  third_party/patches/apple-private-apis/fallible-gsa-token-header-parsing.patch \
  icloud-auth/src/client.rs '^fn parse_token_header\('
rp_patch "keychain self-exclusion" "$RP/src/icloud/keychain.rs" \
  's/^            for excluded in &trust\.excludeds \{$/            let my_id = &state.user_identity.as_ref().unwrap().identifier;\n            for excluded in &trust.excludeds {\n                if excluded == my_id {\n                    warn!(\n                        "Ignoring exclusion of ourselves ({}) from peer {}",\n                        excluded,\n                        peer.0.hash.as_ref().unwrap()\n                    );\n                    continue;\n                }/' \
  'Ignoring exclusion of ourselves'
rp_patch "keychain passcode_generation default" "$RP/src/icloud/keychain.rs" \
  's/^    pub passcode_generation: u32,$/    #[serde(default)] pub passcode_generation: u32,/' \
  '#\[serde\(default\)\] pub passcode_generation: u32,'
rp_patch "REGISTER body XML dump" "$RP/src/ids/user.rs" \
  's/^    let mut request = SignedRequest::new\("id-register", Method::POST\)$/    if std::env::var("RUSTPUSH_LOG_REGISTER_BODY").is_ok() { info!("REGISTER body XML: {}", plist_to_string(&body).unwrap_or_default()); } let mut request = SignedRequest::new("id-register", Method::POST)/' \
  'RUSTPUSH_LOG_REGISTER_BODY'
rp_patch "statuskit no-saved-channel panic" "$RP/src/statuskit.rs" \
  's/            panic!\("No saved channel for identifier!"\)$/            warn!("StatusKit: no saved channel for identifier - using last_msg_ns=0 (will replay)"); return APSChannel { identifier: channel.clone(), last_msg_ns: 0, subscribe: join };/' \
  'using last_msg_ns=0 \(will replay\)'
rp_patch "statuskit channel-not-found panic" "$RP/src/statuskit.rs" \
  's/let Some\(referenced_channel\) = state\.keys\.get_mut\(&base64_encode\(&channel\.id\)\) else \{ panic!\("Channel not found!"\) \};/let Some(referenced_channel) = state.keys.get_mut(&base64_encode(&channel.id)) else { warn!("StatusKit: presence msg arrived before keysharing for channel={} - dropping", encode_hex(&channel.id)); return Ok(None); };/' \
  'presence msg arrived before keysharing'
# macoun: reregister IDS 7 days before certificate expiry instead of 5 minutes,
# cap the Identity retry backoff at 30 minutes instead of a day, and log
# register failures at warn. Lets the bridge ride out a Mac relay outage.
rp_apply "macoun IDS rereg lead, backoff cap, warn" "$RP" \
  third_party/patches/rustpush/macoun-ids-rereg-and-backoff.patch \
  src/ids/user.rs '^pub fn macoun_rereg_in_s\('
echo "$stamp" > "$RP/.macoun-prepared"
echo "rustpush prepared at $RP ($PIN)"
