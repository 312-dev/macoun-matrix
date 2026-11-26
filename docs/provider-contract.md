# Provider contract

The bridge gets NAC validation data and anisette headers from HTTP providers.
`tools/macoun-relay` on a Mac is the documented default, but any service that
implements this contract can be plugged in, for either role or both.

## Configuration

| Role | Variables | Notes |
|---|---|---|
| Validation data (and anisette, unless overridden) | `MACOUN_NAC_RELAY_URL`, `MACOUN_NAC_RELAY_TOKEN`, `MACOUN_NAC_RELAY_CERT_FP` | Can also come from `nac_relay_url`, `relay_token`, `relay_cert_fp` in the hardware key JSON; the environment wins. `/validation-data` is appended to the URL when missing. |
| Anisette from a different provider | `MACOUN_ANISETTE_PROVIDER_URL`, `MACOUN_ANISETTE_PROVIDER_TOKEN`, `MACOUN_ANISETTE_PROVIDER_CERT_FP` | Optional. Unset means anisette comes from the validation provider. |
| Anisette from an anisette v3 server | `MACOUN_ANISETTE_URL` | Last resort, https only. Public v3 servers run Android ADI, which Apple refuses for a Mac identity. |

With no anisette source configured, anisette calls fail with an error that
names these variables. There is no built-in public fallback.

## Transport and authentication

- HTTPS only.
- `*_CERT_FP` set: the SHA-256 of the server's DER certificate, as hex (colons
  and case are ignored). Chain validation is off and the fingerprint is
  enforced. Before any authenticated request the bridge makes an
  unauthenticated `GET <base>/health` and checks the fingerprint, so the token
  is never sent to the wrong server; every later response is checked too.
- `*_CERT_FP` unset: the certificate must chain to a public CA.
- `*_TOKEN` set: sent as `Authorization: Bearer <token>`. A provider should
  answer `401` to a missing or wrong token.

`<base>` is the URL without a trailing `/validation-data`.

## Endpoints

### `POST <base>/validation-data`

- Request: empty body, bearer token.
- Success: `200`, `Content-Type: text/plain`, body is the base64 (standard
  alphabet, padded) of the complete NAC validation data blob that IDS
  registration and Apple ID delegate login send to Apple. The provider does the
  whole NAC exchange with Apple itself.
- Failure: any non-2xx status; the body is shown in the bridge log.
- Called at IDS registration (every 45 days, or when Apple asks), at login and
  at the weekly MobileMe refresh. Expect a handful of calls a week.

### `GET <base>/anisette`

- Request: bearer token.
- Success: `200`, `Content-Type: application/json`, an object of header name
  to string value, for example:

  ```json
  {
    "X-Apple-I-MD": "<base64 one-time code>",
    "X-Apple-I-MD-M": "<base64 machine id>",
    "X-Apple-I-MD-LU": "<local user uuid>",
    "X-Apple-SRL-NO": "<serial>",
    "X-Mme-Client-Info": "<Mac model> <macOS;version;build> <com.apple.AuthKit/1 (com.apple.akd/1.0)>",
    "X-Mme-Device-Id": "<device uuid>",
    "X-Apple-Locale": "en_US",
    "X-Apple-I-Client-Time": "2026-11-25T12:00:00Z",
    "X-Apple-I-TimeZone": "UTC"
  }
  ```

  `X-Apple-I-MD` and `X-Apple-I-MD-M` are required and must be non-empty; the
  bridge rejects a response without them. The others are passed through to
  Apple as given, so they should describe the same device as the hardware key.
- Failure: any non-2xx status.
- Called whenever the bridge needs fresh anisette (results are cached for 60
  seconds): at startup, at Apple ID token refreshes (about 7 a day), and on
  CloudKit and iCloud calls.

### `GET <base>/health`

- No authentication; must not reveal secrets.
- The bridge only uses it to check the certificate pin; it does not read the
  body or status.
- macoun-relay answers `200` when healthy and `503` otherwise, with JSON:

  ```json
  {
    "ok": true,
    "nac": {"ok": true, "framework_ok": true, "last_success": "...", "last_attempt": "..."},
    "anisette": {"ok": true, "header_count": 9, "checked_at": "..."},
    "os_build": "..."
  }
  ```

  On failure, `nac.framework_error`, `nac.last_error`, `nac.last_error_at` and
  `anisette.error` explain why. `examples/monitoring/` probes this endpoint.

## Writing your own provider

Keep it private (bind to a private network, require the token), keep the
device identity consistent with the hardware key the bridge logged in with,
and keep call volume low: Apple sees every NAC exchange.
