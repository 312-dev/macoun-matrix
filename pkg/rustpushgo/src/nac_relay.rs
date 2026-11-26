// SPDX-License-Identifier: AGPL-3.0-only
//
// NAC relay client: fetch IDS validation data from a Mac over HTTPS.
//
// `RelayOSConfig` is taken from corten-matrix commit e2adc79d92 (2026-06-21,
// pkg/rustpushgo/src/lib.rs), which was published under the GNU Affero General
// Public License v3. This file stays under AGPL-3.0; see NOTICE.md. macoun-matrix
// changes: the relay settings can come from the environment, the TLS
// certificate fingerprint carried in the hardware key is enforced rather than
// ignored, and `get_adi_mme_info` is delegated as well.
//
// Protocol (tools/nac-relay at e2adc79d92, running on the Mac):
//   POST <url>/validation-data, `Authorization: Bearer <token>`
//     -> 200, text/plain body = base64 of the complete validation data
//   GET  <url>/health -> "ok" (no auth)
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

/// Wraps the hardware-key `MacOSConfig`; only `generate_validation_data` is
/// replaced, every other method delegates unchanged.
pub struct RelayOSConfig {
    pub inner: Arc<rustpush::macos::MacOSConfig>,
    pub relay: RelaySettings,
}

impl RelayOSConfig {
    pub fn new(inner: Arc<rustpush::macos::MacOSConfig>, relay: RelaySettings) -> Self {
        if relay.cert_fp.is_none() {
            warn!("NAC relay at {} has no pinned certificate fingerprint; any certificate is accepted", relay.url);
        }
        if relay.token.is_none() {
            warn!("NAC relay at {} has no bearer token", relay.url);
        }
        Self { inner, relay }
    }
}

#[async_trait::async_trait]
impl OSConfig for RelayOSConfig {
    fn build_activation_info(&self, csr: Vec<u8>) -> rustpush::activation::ActivationInfo {
        self.inner.build_activation_info(csr)
    }
    fn get_activation_device(&self) -> String { self.inner.get_activation_device() }
    async fn generate_validation_data(&self) -> Result<Vec<u8>, PushError> {
        use base64::{engine::general_purpose::STANDARD, Engine};

        // The relay uses a self-signed certificate, so chain validation is
        // off; the fingerprint pin below is what authenticates the server.
        let client = reqwest::Client::builder()
            .danger_accept_invalid_certs(true)
            .tls_info(true)
            .timeout(std::time::Duration::from_secs(60))
            .build()
            .map_err(|e| PushError::RelayError(0, format!("Failed to build relay client: {e}")))?;

        // Verify the pin on an unauthenticated request first, so the bearer
        // token is not sent to a server that fails it.
        if self.relay.cert_fp.is_some() {
            let health = self.relay.url.trim_end_matches("/validation-data").to_string() + "/health";
            let resp = client.get(&health).send().await
                .map_err(|e| PushError::RelayError(0, format!("NAC relay health check failed: {e}")))?;
            check_pin(&resp, &self.relay.cert_fp)?;
        }

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
    fn resolve_none_without_url() {
        if std::env::var(ENV_URL).is_err() {
            assert!(resolve(None, Some("t".into()), None).is_none());
            assert!(resolve(Some("  ".into()), None, None).is_none());
        }
    }
}
