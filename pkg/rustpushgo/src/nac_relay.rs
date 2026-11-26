// SPDX-License-Identifier: AGPL-3.0-only
//
// Validation-data and anisette provider client: fetch IDS validation data and
// anisette headers over HTTPS from the Mac relay (tools/macoun-relay) or any
// service that speaks the same contract (docs/provider-contract.md).
//
// `RelayOSConfig` is taken from corten-matrix commit e2adc79d92 (2026-06-21,
// pkg/rustpushgo/src/lib.rs), which was published under the GNU Affero General
// Public License v3. This file stays under AGPL-3.0; see NOTICE.md. macoun-matrix
// changes: the relay settings can come from the environment, the TLS
// certificate fingerprint carried in the hardware key is enforced rather than
// ignored, `get_adi_mme_info` is delegated as well, and the same relay also
// serves anisette headers (`fetch_anisette_headers`).
//
// Protocol (tools/macoun-relay, from corten-matrix e2adc79d92 tools/nac-relay):
//   POST <url>/validation-data, `Authorization: Bearer <token>`
//     -> 200, text/plain body = base64 of the complete validation data
//   GET  <url>/health -> "ok" (no auth)
//   GET  <url>/anisette, same auth (macoun-matrix addition, see
//     fetch_anisette_headers) -> 200, JSON object of anisette headers
// The Mac runs the whole NAC init / Apple initializeValidation / sign sequence
// through AAAbsintheContext, so no NAC code runs on Linux at all.

use std::collections::HashMap;
use std::sync::Arc;

use log::{info, warn};
use rustpush::{OSConfig, PushError};

/// Relay settings resolved from the hardware key and the environment.
#[derive(Clone, Debug, Default)]
pub struct RelaySettings {
    pub url: String,
    pub token: Option<String>,
    /// SHA-256 of the relay's DER certificate, hex. None disables pinning.
    pub cert_fp: Option<String>,
}

/// Environment overrides. Each one, when set and non-empty, replaces the
/// matching hardware-key field, so an existing login can be pointed at a relay
/// without rewriting the key stored in the bridge database.
pub const ENV_URL: &str = "MACOUN_NAC_RELAY_URL";
pub const ENV_TOKEN: &str = "MACOUN_NAC_RELAY_TOKEN";
pub const ENV_CERT_FP: &str = "MACOUN_NAC_RELAY_CERT_FP";

fn env_nonempty(name: &str) -> Option<String> {
    std::env::var(name).ok().map(|v| v.trim().to_string()).filter(|v| !v.is_empty())
}

/// Combine key fields with environment overrides. Returns None when no relay
/// URL is configured either way.
pub fn resolve(
    key_url: Option<String>,
    key_token: Option<String>,
    key_cert_fp: Option<String>,
) -> Option<RelaySettings> {
    let nonempty = |v: Option<String>| v.map(|s| s.trim().to_string()).filter(|s| !s.is_empty());
    let url = env_nonempty(ENV_URL).or(nonempty(key_url))?;
    let token = env_nonempty(ENV_TOKEN).or(nonempty(key_token));
    let cert_fp = env_nonempty(ENV_CERT_FP).or(nonempty(key_cert_fp)).map(|fp| normalize_fp(&fp));
    let trimmed = url.trim_end_matches('/');
    let url = if trimmed.ends_with("/validation-data") {
        trimmed.to_string()
    } else {
        format!("{}/validation-data", trimmed)
    };
    Some(RelaySettings { url, token, cert_fp })
}

/// Lowercase hex with any ':' separators removed.
pub fn normalize_fp(fp: &str) -> String {
    fp.chars().filter(|c| *c != ':' && !c.is_whitespace()).collect::<String>().to_lowercase()
}

pub fn sha256_hex(der: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    hex::encode(Sha256::digest(der))
}

/// Check the certificate the relay presented against the pinned fingerprint.
fn check_pin(resp: &reqwest::Response, want: &Option<String>) -> Result<(), PushError> {
    let Some(want) = want else { return Ok(()) };
    let got = resp
        .extensions()
        .get::<reqwest::tls::TlsInfo>()
        .and_then(|info| info.peer_certificate())
        .map(sha256_hex);
    match got {
        Some(ref fp) if fp == want => Ok(()),
        Some(fp) => Err(PushError::RelayError(0, format!(
            "NAC relay certificate fingerprint {fp} does not match the pinned {want}"
        ))),
        None => Err(PushError::RelayError(0,
            "NAC relay presented no TLS certificate, but a fingerprint is pinned".to_string())),
    }
}

impl RelaySettings {
    /// The relay's base URL, without the `/validation-data` endpoint.
    pub fn base_url(&self) -> &str {
        self.url.trim_end_matches("/validation-data")
    }
}

/// Build a client for a provider. With a pinned fingerprint (the Mac relay's
/// self-signed certificate), chain validation is off and the pin is what
/// authenticates the server: it is checked on an unauthenticated `/health`
/// request first, so the bearer token is never sent to a server that fails
/// it, and on every later response. Without a pin, the certificate must chain
/// to a public CA (a remote provider with a normal certificate).
async fn relay_client(relay: &RelaySettings) -> Result<reqwest::Client, PushError> {
    let client = reqwest::Client::builder()
        .danger_accept_invalid_certs(relay.cert_fp.is_some())
        .tls_info(true)
        .timeout(std::time::Duration::from_secs(60))
        .build()
        .map_err(|e| PushError::RelayError(0, format!("Failed to build relay client: {e}")))?;
    if relay.cert_fp.is_some() {
        let health = format!("{}/health", relay.base_url());
        let resp = client.get(&health).send().await
            .map_err(|e| PushError::RelayError(0, format!("NAC relay health check failed: {e}")))?;
        check_pin(&resp, &relay.cert_fp)?;
    }
    Ok(client)
}

// The relay this process registered with, set when a hardware-key config with
// a relay is built. The anisette provider (anisette.rs) reads it, because
// rustpush constructs anisette providers without access to the OSConfig.
static ACTIVE: std::sync::RwLock<Option<RelaySettings>> = std::sync::RwLock::new(None);

pub fn set_active(relay: &RelaySettings) {
    if let Ok(mut a) = ACTIVE.write() {
        *a = Some(relay.clone());
    }
}

/// The registered relay, or one configured only through MACOUN_NAC_RELAY_*.
pub fn active() -> Option<RelaySettings> {
    ACTIVE.read().ok().and_then(|a| a.clone()).or_else(|| resolve(None, None, None))
}

/// A separate anisette provider speaking the same contract (`GET /anisette`,
/// same auth), for setups where anisette comes from a different service than
/// validation data. Unset: anisette comes from the validation provider.
pub const ENV_ANISETTE_PROVIDER_URL: &str = "MACOUN_ANISETTE_PROVIDER_URL";
pub const ENV_ANISETTE_PROVIDER_TOKEN: &str = "MACOUN_ANISETTE_PROVIDER_TOKEN";
pub const ENV_ANISETTE_PROVIDER_CERT_FP: &str = "MACOUN_ANISETTE_PROVIDER_CERT_FP";

pub fn anisette_provider() -> Option<RelaySettings> {
    match env_nonempty(ENV_ANISETTE_PROVIDER_URL) {
        Some(url) => Some(RelaySettings {
            url: url.trim_end_matches('/').to_string(),
            token: env_nonempty(ENV_ANISETTE_PROVIDER_TOKEN),
            cert_fp: env_nonempty(ENV_ANISETTE_PROVIDER_CERT_FP).map(|fp| normalize_fp(&fp)),
        }),
        None => active(),
    }
}

/// Anisette headers from the relay's `GET /anisette` endpoint (macoun-matrix
/// extension of the relay protocol): a JSON object of header name to value,
/// produced on the Mac by AOSKit (`AOSUtilities retrieveOTPHeadersForDSID:`
/// plus the `AKDevice` fields, as omnisette's aos_kit.rs does on macOS). The
/// OTP then comes from the same real Mac as the hardware key's identity.
pub async fn fetch_anisette_headers(relay: &RelaySettings) -> Result<HashMap<String, String>, PushError> {
    let client = relay_client(relay).await?;
    let url = format!("{}/anisette", relay.base_url());
    let mut req = client.get(&url);
    if let Some(ref token) = relay.token {
        req = req.header("Authorization", format!("Bearer {token}"));
    }
    let resp = req.send().await
        .map_err(|e| PushError::RelayError(0, format!("anisette relay request failed: {e}")))?;
    check_pin(&resp, &relay.cert_fp)?;
    if !resp.status().is_success() {
        let status = resp.status().as_u16();
        let body = resp.text().await.unwrap_or_default();
        return Err(PushError::RelayError(status, format!("anisette relay error: {body}")));
    }
    let body = resp.text().await
        .map_err(|e| PushError::RelayError(0, format!("anisette relay read error: {e}")))?;
    parse_anisette_headers(&body)
}

/// Parse and sanity-check the relay's header map. X-Apple-I-MD (the OTP) and
/// X-Apple-I-MD-M (the machine id) are required; without them every
/// GrandSlam call would fail later with a less obvious error.
pub fn parse_anisette_headers(body: &str) -> Result<HashMap<String, String>, PushError> {
    let headers: HashMap<String, String> = serde_json::from_str(body)
        .map_err(|e| PushError::RelayError(0, format!("anisette relay returned invalid JSON: {e}")))?;
    for required in ["X-Apple-I-MD", "X-Apple-I-MD-M"] {
        if headers.get(required).map_or(true, |v| v.is_empty()) {
            return Err(PushError::RelayError(0, format!("anisette relay response lacks {required}")));
        }
    }
    Ok(headers)
}

/// Wraps the hardware-key `MacOSConfig`; only `generate_validation_data` is
/// replaced, every other method delegates unchanged.
pub struct RelayOSConfig {
    pub inner: Arc<rustpush::macos::MacOSConfig>,
    pub relay: RelaySettings,
}

impl RelayOSConfig {
    pub fn new(inner: Arc<rustpush::macos::MacOSConfig>, relay: RelaySettings) -> Self {
        if relay.cert_fp.is_none() {
            info!("NAC relay at {} has no pinned certificate fingerprint; its certificate must chain to a public CA", relay.url);
        }
        if relay.token.is_none() {
            warn!("NAC relay at {} has no bearer token", relay.url);
        }
        set_active(&relay);
        Self { inner, relay }
    }

    async fn fetch_validation_data(&self) -> Result<Vec<u8>, PushError> {
        use base64::{engine::general_purpose::STANDARD, Engine};

        // The relay uses a self-signed certificate, so chain validation is
        // off; the fingerprint pin below is what authenticates the server.
        let client = relay_client(&self.relay).await?;

        let mut req = client.post(&self.relay.url);
        if let Some(ref token) = self.relay.token {
            req = req.header("Authorization", format!("Bearer {token}"));
        }
        let resp = req.send().await
            .map_err(|e| PushError::RelayError(0, format!("NAC relay request failed: {e}")))?;
        check_pin(&resp, &self.relay.cert_fp)?;

        if !resp.status().is_success() {
            let status = resp.status().as_u16();
            let body = resp.text().await.unwrap_or_default();
            return Err(PushError::RelayError(status, format!("NAC relay error: {body}")));
        }
        let b64 = resp.text().await
            .map_err(|e| PushError::RelayError(0, format!("NAC relay read error: {e}")))?;
        let data = STANDARD.decode(b64.trim())
            .map_err(|e| PushError::RelayError(0, format!("NAC relay base64 decode: {e}")))?;
        info!("NAC relay: got {} bytes of validation data from {}", data.len(), self.relay.url);
        Ok(data)
    }
}

#[async_trait::async_trait]
impl OSConfig for RelayOSConfig {
    fn build_activation_info(&self, csr: Vec<u8>) -> rustpush::activation::ActivationInfo {
        self.inner.build_activation_info(csr)
    }
    fn get_activation_device(&self) -> String { self.inner.get_activation_device() }
    async fn generate_validation_data(&self) -> Result<Vec<u8>, PushError> {
        // Warn here: rustpush's callers log a failed register only at debug
        // upstream, and a relay outage must show in the default log.
        self.fetch_validation_data().await.map_err(|e| {
            warn!("NAC relay: validation data from {} failed: {}", self.relay.url, e);
            e
        })
    }
    fn get_protocol_version(&self) -> u32 { self.inner.get_protocol_version() }
    fn get_register_meta(&self) -> rustpush::RegisterMeta { self.inner.get_register_meta() }
    fn get_normal_ua(&self, item: &str) -> String { self.inner.get_normal_ua(item) }
    fn get_mme_clientinfo(&self, for_item: &str) -> String { self.inner.get_mme_clientinfo(for_item) }
    fn get_version_ua(&self) -> String { self.inner.get_version_ua() }
    fn get_device_name(&self) -> String { self.inner.get_device_name() }
    fn get_device_uuid(&self) -> String { self.inner.get_device_uuid() }
    fn get_private_data(&self) -> plist::Dictionary { self.inner.get_private_data() }
    fn get_debug_meta(&self) -> rustpush::DebugMeta { self.inner.get_debug_meta() }
    fn get_login_url(&self) -> &'static str { self.inner.get_login_url() }
    fn get_serial_number(&self) -> String { self.inner.get_serial_number() }
    fn get_gsa_hardware_headers(&self) -> HashMap<String, String> { self.inner.get_gsa_hardware_headers() }
    fn get_aoskit_version(&self) -> String { self.inner.get_aoskit_version() }
    fn get_udid(&self) -> String { self.inner.get_udid() }
    fn get_adi_mme_info(&self, for_item: &str, require_mac: bool) -> String {
        self.inner.get_adi_mme_info(for_item, require_mac)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn resolve_appends_endpoint_and_normalizes_fp() {
        let s = resolve(Some("https://100.64.0.1:5001/".into()), Some("t".into()), Some("AB:cd:EF".into())).unwrap();
        assert_eq!(s.url, "https://100.64.0.1:5001/validation-data");
        assert_eq!(s.cert_fp.as_deref(), Some("abcdef"));
        let s = resolve(Some("https://h:5001/validation-data".into()), None, None).unwrap();
        assert_eq!(s.url, "https://h:5001/validation-data");
    }

    #[test]
    fn separate_anisette_provider_from_env() {
        std::env::set_var(ENV_ANISETTE_PROVIDER_URL, "https://anisette.example/");
        std::env::set_var(ENV_ANISETTE_PROVIDER_CERT_FP, "AA:BB");
        let p = anisette_provider().unwrap();
        std::env::remove_var(ENV_ANISETTE_PROVIDER_URL);
        std::env::remove_var(ENV_ANISETTE_PROVIDER_CERT_FP);
        assert_eq!(p.base_url(), "https://anisette.example");
        assert_eq!(p.cert_fp.as_deref(), Some("aabb"));
        assert!(p.token.is_none());
    }

    #[test]
    fn base_url_strips_endpoint() {
        let s = resolve(Some("https://h:5001".into()), None, None).unwrap();
        assert_eq!(s.base_url(), "https://h:5001");
    }

    #[test]
    fn anisette_headers_require_otp_and_machine_id() {
        let ok = parse_anisette_headers(r#"{"X-Apple-I-MD":"otp","X-Apple-I-MD-M":"mid","X-Mme-Device-Id":"d"}"#).unwrap();
        assert_eq!(ok.get("X-Apple-I-MD-M").map(String::as_str), Some("mid"));
        assert!(parse_anisette_headers(r#"{"X-Apple-I-MD":"otp"}"#).is_err());
        assert!(parse_anisette_headers(r#"{"X-Apple-I-MD":"","X-Apple-I-MD-M":"m"}"#).is_err());
        assert!(parse_anisette_headers("not json").is_err());
    }

    #[test]
    fn resolve_none_without_url() {
        if std::env::var(ENV_URL).is_err() {
            assert!(resolve(None, Some("t".into()), None).is_none());
            assert!(resolve(Some("  ".into()), None, None).is_none());
        }
    }
}
