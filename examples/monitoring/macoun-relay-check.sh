#!/usr/bin/env bash
# macoun-relay-check: watch the Mac relay that gives macoun-matrix NAC validation
# data and Apple anisette headers.
#
#   macoun-relay-check.sh health   every 5 min: GET /health over Tailscale, TLS key pinned.
#                                  Alerts once the relay has failed for more than GRACE
#                                  seconds (default 15 min), repeats every 6 h while down,
#                                  and sends a recovery note. Also notes a macOS build change.
#   macoun-relay-check.sh daily    once a day: days until the IDS identity certs expire
#                                  (the bridge must re-register through the relay before
#                                  then) and days until the Mac's Tailscale node key expires.
#
# Alerts go to an ntfy topic URL read from NTFY_FILE (example; any webhook works
# with a small change to alert()). /health carries no secrets and needs no token. Nothing here
# reads or prints a secret: session.json is opened read-only and only cert dates leave it.
# Set DRY_RUN=1 to print alerts instead of sending them.
set -u

MODE="${1:-health}"
URL="${MACOUN_RELAY_URL:?set MACOUN_RELAY_URL, e.g. https://<mac-tailscale-ip>:5001}"
# SHA-256 of the relay cert's public key (SPKI), for curl --pinnedpubkey. The bridge pins
# the whole-cert SHA-256 (MACOUN_NAC_RELAY_CERT_FP) instead; both change only if the
# relay's cert.pem/key.pem are regenerated. See docs/setup.md for how to compute it.
PIN="${MACOUN_RELAY_PIN:?set MACOUN_RELAY_PIN=sha256//<base64 SPKI hash>}"
STATE="${MACOUN_STATE_DIR:-/var/lib}/macoun-relay"
GRACE="${MACOUN_RELAY_GRACE_S:-900}"
REPEAT="${MACOUN_RELAY_REPEAT_S:-21600}"
SESSION_JSON="${MACOUN_SESSION_JSON:-}"  # the bridge data dir session.json (daily mode)
MINI_TS_NAME="${MACOUN_MINI_TS_NAME:-mac-relay}"
NTFY="$(cat "${NTFY_FILE:-/etc/macoun/ntfy-url}" 2>/dev/null || true)"
NOW="${MACOUN_NOW:-$(date +%s)}"

mkdir -p "$STATE"

# alert <title> <message> [priority] [tags]
alert() {
  local prio="${3:-high}" tags="${4:-warning}"
  if [ "${DRY_RUN:-0}" = 1 ]; then
    printf 'ALERT [%s] %s: %s\n' "$prio" "$1" "$2"
    return
  fi
  [ -n "$NTFY" ] && curl -s -m 20 -H "Title: $1" -H "Priority: $prio" -H "Tags: $tags" \
    -d "$2" "$NTFY" >/dev/null 2>&1 || true
}

check_health() {
  local resp code body summary ok
  resp="$(curl -s -k --pinnedpubkey "$PIN" -m 20 -w '\n%{http_code}' "$URL/health" 2>/dev/null)"
  code="${resp##*$'\n'}"
  body="${resp%$'\n'*}"
  # One line: ok flag, then a short reason built only from /health's own fields.
  summary="$(printf '%s' "$body" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    print("false no JSON body"); sys.exit()
n, a = d.get("nac", {}), d.get("anisette", {})
why = []
if not n.get("framework_ok"): why.append("NAC framework: %s" % n.get("framework_error", "?"))
if n.get("last_error"): why.append("NAC: %s" % n["last_error"])
if not a.get("ok"): why.append("anisette: %s" % a.get("error", "?"))
print("true" if d.get("ok") else "false", "; ".join(why) or "ok", "| os_build", d.get("os_build", "?"))
' 2>/dev/null)"
  ok="${summary%% *}"
  summary="${summary#* }"
  [ "$code" = 000 ] && summary="unreachable (TCP/TLS/pin failure or Mac asleep/offline)"

  if [ "$code" = 200 ] && [ "$ok" = true ]; then
    if [ -f "$STATE/down_since" ]; then
      local since; since="$(cat "$STATE/down_since")"
      [ -f "$STATE/alerted_at" ] && alert "macoun relay recovered" \
        "Mac relay healthy again after $(( (NOW - since) / 60 )) min." default white_check_mark
      rm -f "$STATE/down_since" "$STATE/alerted_at"
    fi
    local build="${summary##*os_build }" last=""
    [ -f "$STATE/os_build" ] && last="$(cat "$STATE/os_build")"
    if [ -n "$last" ] && [ "$build" != "$last" ]; then
      alert "macoun relay: macOS changed" "Mac macOS build $last -> $build. /health is green, but run one real /validation-data call to confirm NAC still works." default
    fi
    printf '%s' "$build" > "$STATE/os_build"
    return 0
  fi

  [ -f "$STATE/down_since" ] || printf '%s' "$NOW" > "$STATE/down_since"
  local since last_alert=0
  since="$(cat "$STATE/down_since")"
  [ -f "$STATE/alerted_at" ] && last_alert="$(cat "$STATE/alerted_at")"
  if [ $(( NOW - since )) -gt "$GRACE" ] && [ $(( NOW - last_alert )) -ge "$REPEAT" ]; then
    alert "macoun relay DOWN" "Mac relay failing for $(( (NOW - since) / 60 )) min (HTTP $code): $summary. iMessage keeps flowing for existing chats; bridge restarts, iCloud sync and the next IDS re-registration need it."
    printf '%s' "$NOW" > "$STATE/alerted_at"
  fi
  return 1
}

check_daily() {
  local days relay_down=0
  [ -f "$STATE/down_since" ] && relay_down=1

  # IDS identity certs: the earliest notAfter across registered services.
  days="$(python3 - "$SESSION_JSON" "$NOW" <<'PY' 2>/dev/null
import json, plistlib, subprocess, sys, datetime
d = json.load(open(sys.argv[1]))
users = plistlib.loads(d["ids_users"].encode())
ends = []
def walk(o, path):
    if isinstance(o, dict):
        for k, v in o.items(): walk(v, path + [str(k)])
    elif isinstance(o, list):
        for v in o: walk(v, path)
    elif isinstance(o, (bytes, bytearray)) and "registration" in path and "id_keypair" in path:
        r = subprocess.run(["openssl", "x509", "-inform", "DER", "-noout", "-enddate"],
                           input=bytes(o), capture_output=True, text=False)
        if r.returncode == 0:
            s = r.stdout.decode().strip().split("=", 1)[1]
            ends.append(datetime.datetime.strptime(s, "%b %d %H:%M:%S %Y GMT").replace(tzinfo=datetime.timezone.utc))
walk(users, [])
if not ends: sys.exit(1)
print("%.1f %s" % ((min(ends).timestamp() - int(sys.argv[2])) / 86400, min(ends).strftime("%Y-%m-%d %H:%M UTC")))
PY
)"
  if [ -z "$days" ]; then
    alert "macoun IDS cert check broken" "Could not read IDS cert dates from $SESSION_JSON. Fix the check; the re-registration deadline is unwatched." default
  else
    local left="${days%% *}" when="${days#* }" whole
    whole="${left%%.*}"
    if [ "$whole" -lt 0 ] || { [ "$whole" -lt 2 ] && [ "$relay_down" = 1 ]; }; then
      alert "macoun IDS cert expiring, relay down" "IDS identity cert ends $when ($left days). The bridge re-registers through the Mac relay 5 min before that; the relay is failing now." urgent rotating_light
    elif [ "$whole" -lt 2 ]; then
      alert "macoun IDS cert expires in $left days" "Ends $when. Relay is healthy; check after that time that the bridge re-registered (the cert date moves ~45 days)." high
    elif [ "$whole" -lt 7 ]; then
      alert "macoun IDS cert expires in $left days" "Ends $when. Keep the Mac awake and online through then." default
    fi
  fi

  # The Mac's Tailscale node key, as the box sees it.
  local ts
  ts="$(tailscale status --json 2>/dev/null | python3 -c '
import json, sys, datetime
peers = json.load(sys.stdin).get("Peer", {}).values()
p = next((p for p in peers if p.get("HostName", "").lower() == sys.argv[1] or p.get("DNSName", "").split(".")[0] == sys.argv[1]), None)
if p is None: print("missing"); sys.exit()
e = p.get("KeyExpiry")
if not e: print("none"); sys.exit()
t = datetime.datetime.fromisoformat(e.replace("Z", "+00:00"))
print("%.1f %s" % ((t.timestamp() - int(sys.argv[2])) / 86400, t.strftime("%Y-%m-%d")))
' "$MINI_TS_NAME" "$NOW" 2>/dev/null)"
  case "$ts" in
    none) ;;
    missing|"") alert "macoun relay: Mac not in tailnet" "The box's tailscale status has no peer named $MINI_TS_NAME." default ;;
    *) local tleft="${ts%% *}"; tleft="${tleft%%.*}"
       if [ "$tleft" -lt 0 ] || [ "${ts:0:1}" = - ]; then
         alert "Mac Tailscale key EXPIRED" "Node key for $MINI_TS_NAME ended ${ts#* }; the relay is unreachable until it is re-authenticated on the Mac. Then disable key expiry for it in the Tailscale admin console." urgent rotating_light
       elif [ "$tleft" -lt 14 ]; then
         alert "Mac Tailscale key expires in ${ts%% *} days" "Node key for $MINI_TS_NAME ends ${ts#* }. Disable key expiry for it in the Tailscale admin console." high
       fi ;;
  esac
}

case "$MODE" in
  health) check_health ;;
  daily) check_daily ;;
  *) echo "usage: $0 health|daily" >&2; exit 2 ;;
esac
