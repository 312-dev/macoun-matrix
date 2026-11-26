//! Linux anisette provider: the Mac relay's `GET /anisette`, or
//! `RemoteAnisetteProviderV3` (see `AnisetteSource` for the order).
//!
//! This module is compiled only with rustpushgo's `anisette-remote-v3` feature.
//! The upstream release Linux build uses the closed clean-room `ClearADIClient`
//! instead; macoun-matrix builds Linux with this module, because that client
//! is not in the public source. The timeout, retry and panic guards below
//! cover both sources.
//!
//! The `RemoteAnisetteProviderV3` provisioning has three bugs we work around here:
//!   1. The `ProvisionInput` enum is missing `EndProvisioningError`, so a
//!      transient Apple rejection crashes serde instead of returning an error.
//!   2. The provision() loop (`let Some(Ok(data)) = ... else { continue }`)
//!      spins forever if the WebSocket stream closes.
//!   3. `get_anisette_headers` contains a bare `panic!()` for any
//!      non-`AnisetteNotProvisioned` error from `get_headers` (see
//!      `remote_anisette_v3.rs:417`). If that panic unwinds across the
//!      uniffi FFI boundary while the caller holds the shared
//!      `tokio::sync::Mutex<anisette>` (TokenProvider, CloudKitClient,
//!      KeychainClient all share it), every subsequent anisette-touching
//!      operation deadlocks — including message send.
//!
//! This wrapper catches those failures, retries, and adds a timeout. All
//! Apple-facing requests go through the provider's code unchanged.
//!
//! STATE PRESERVATION: we DO NOT automatically delete state.plist on
//! transient errors. The anisette state carries the device identity
//! (`X-Apple-I-MD-M`, `X-Apple-I-MD-LU`, etc.) that Apple uses to recognize
//! this bridge as a trusted device. Re-provisioning generates a fresh
//! identity, which Apple treats as a new device — forcing 2FA on the next
//! `login_email_pass` call. Historically this wrapper wiped state on any
//! serde error / panic / timeout, which caused identity churn whenever
//! the provider transiently flaked, and that manifested as daily NeedsDevice2FA
//! failures on every CloudKit auth cycle at the 24h PET-expiration boundary.
//!
//! If a user genuinely wants a fresh anisette identity (suspecting state
//! corruption), they can delete `state/anisette/state.plist` manually and
//! accept the one-time 2FA prompt on next auth.

use std::collections::HashMap;
use std::panic::AssertUnwindSafe;
use std::path::PathBuf;
use std::time::Duration;

use futures::FutureExt;
use log::{error, info, warn};
use omnisette::remote_anisette_v3::RemoteAnisetteProviderV3;
use omnisette::{AnisetteError, AnisetteProvider, LoginClientInfo};

const ANISETTE_URL_ENV: &str = "MACOUN_ANISETTE_URL";

/// Where anisette headers come from, in order of preference:
///
/// 1. An anisette provider speaking the macoun contract (`GET /anisette`):
///    MACOUN_ANISETTE_PROVIDER_URL if set, else the NAC relay named by the
///    hardware key or MACOUN_NAC_RELAY_*. On the Mac relay, AOSKit produces
///    the headers.
///    This is the only source that works for a Mac identity since Apple began
///    rejecting Android-ADI OTPs (about 2026-10-12, error -29003).
/// 2. An anisette v3 server named by MACOUN_ANISETTE_URL, through upstream's
///    `RemoteAnisetteProviderV3`. Kept for completeness; every public v3 server
///    runs Android ADI and is currently refused by Apple.
///
/// With neither configured there is deliberately no fallback to a public
/// server: the call fails and says what to set.
#[derive(Clone)]
enum AnisetteSource {
    #[cfg(feature = "hardware-key")]
    Relay(crate::nac_relay::RelaySettings),
    V3(String),
}

fn anisette_source() -> Result<AnisetteSource, AnisetteError> {
    #[cfg(feature = "hardware-key")]
    if let Some(relay) = crate::nac_relay::anisette_provider() {
        return Ok(AnisetteSource::Relay(relay));
    }
    match std::env::var(ANISETTE_URL_ENV) {
        Ok(v) if !v.trim().is_empty() => {
            let url = v.trim().trim_end_matches('/').to_string();
            // RemoteAnisetteProviderV3 derives its provisioning websocket URL
            // with .replace("https://", "wss://") only, so an http:// URL stays
            // http:// and tungstenite rejects it with UnsupportedUrlScheme on
            // first provisioning. Refuse it up front. The websocket also checks
            // webpki roots, so the https certificate must be publicly trusted.
            if !url.starts_with("https://") {
                return Err(AnisetteError::InvalidArgument(format!(
                    "{ANISETTE_URL_ENV}={url} must be an https:// URL with a publicly trusted certificate \
                     (the v3 provisioning websocket maps only https:// to wss://)"
                )));
            }
            Ok(AnisetteSource::V3(url))
        }
        _ => Err(AnisetteError::InvalidArgument(format!(
            "no anisette source configured: set MACOUN_NAC_RELAY_URL (or give the hardware key a \
             relay) so the relay serves anisette, or MACOUN_ANISETTE_PROVIDER_URL, or {ANISETTE_URL_ENV}"
        ))),
    }
}

#[cfg(feature = "hardware-key")]
async fn relay_headers(relay: crate::nac_relay::RelaySettings) -> Result<HashMap<String, String>, AnisetteError> {
    crate::nac_relay::fetch_anisette_headers(&relay)
        .await
        .map_err(|e| AnisetteError::InvalidArgument(format!("anisette from the Mac relay failed: {e}")))
}
const PROVISION_TIMEOUT: Duration = Duration::from_secs(30);
const MAX_RETRIES: usize = 3;

pub struct BridgeAnisetteProvider {
    info: LoginClientInfo,
    state_path: PathBuf,
}

impl BridgeAnisetteProvider {
    pub fn new(info: LoginClientInfo, state_path: PathBuf) -> Self {
        Self { info, state_path }
    }
}

impl AnisetteProvider for BridgeAnisetteProvider {
    fn get_anisette_headers(
        &mut self,
    ) -> impl std::future::Future<Output = Result<HashMap<String, String>, AnisetteError>> + Send
    {
        async move {
            let mut last_err = None;
            let call_start = std::time::Instant::now();
            let source = anisette_source().map_err(|e| {
                error!("anisette: {}", e);
                e
            })?;

            for attempt in 0..MAX_RETRIES {
                let attempt_start = std::time::Instant::now();
                info!(
                    "anisette: starting attempt {}/{} (total elapsed {:?})",
                    attempt + 1,
                    MAX_RETRIES,
                    call_start.elapsed()
                );

                let fetch: futures::future::BoxFuture<'static, Result<HashMap<String, String>, AnisetteError>> =
                    match source.clone() {
                        #[cfg(feature = "hardware-key")]
                        AnisetteSource::Relay(relay) => relay_headers(relay).boxed(),
                        AnisetteSource::V3(url) => {
                            // Fresh provider each attempt — it reads state from disk so a
                            // cleared state.plist forces re-provisioning.
                            let mut upstream = RemoteAnisetteProviderV3::new(
                                url,
                                self.info.clone(),
                                self.state_path.clone(),
                            );
                            async move { upstream.get_anisette_headers().await }.boxed()
                        }
                    };

                // AssertUnwindSafe + catch_unwind turns the provider's bare
                // `panic!()` into a caught panic payload. Without this the
                // panic unwinds into the caller's critical section and can
                // leave shared mutexes locked.
                let inner = AssertUnwindSafe(fetch).catch_unwind();
                // Preserve state.plist across every failure mode — wiping it
                // mid-retry generates a fresh device identity on the next
                // attempt, which Apple sees as a brand-new device and rejects
                // the next login_email_pass with NeedsDevice2FA. The transient
                // provider errors below don't correlate with genuine state
                // corruption, so we leave state alone and just retry.
                match tokio::time::timeout(PROVISION_TIMEOUT, inner).await {
                    Ok(Ok(Ok(headers))) => {
                        info!(
                            "anisette: attempt {}/{} succeeded in {:?} (total {:?})",
                            attempt + 1,
                            MAX_RETRIES,
                            attempt_start.elapsed(),
                            call_start.elapsed()
                        );
                        return Ok(headers);
                    }
                    Ok(Ok(Err(AnisetteError::SerdeError(ref e)))) => {
                        warn!(
                            "anisette: upstream serde error on attempt {}/{} (state preserved): {}",
                            attempt + 1,
                            MAX_RETRIES,
                            e
                        );
                        last_err = Some(AnisetteError::InvalidArgument(format!(
                            "Anisette provisioning was rejected by the server \
                             (attempt {}/{}). Error: {}",
                            attempt + 1,
                            MAX_RETRIES,
                            e
                        )));
                    }
                    Ok(Ok(Err(e))) => {
                        // Non-serde error — don't retry blindly.
                        // macoun: warn with the reason (e.g. Mac relay unreachable).
                        warn!("anisette: attempt {}/{} failed: {}", attempt + 1, MAX_RETRIES, e);
                        return Err(e);
                    }
                    Ok(Err(panic_payload)) => {
                        // `RemoteAnisetteProviderV3::get_anisette_headers`
                        // contains `panic!()` for non-`AnisetteNotProvisioned`
                        // errors. Convert to a retryable error so the panic
                        // doesn't unwind past this point. State preserved —
                        // the panic is a control-flow bug in the provider, not a
                        // sign that our identity is invalid.
                        let msg = if let Some(s) = panic_payload.downcast_ref::<&'static str>() {
                            (*s).to_string()
                        } else if let Some(s) = panic_payload.downcast_ref::<String>() {
                            s.clone()
                        } else {
                            "unknown panic payload".into()
                        };
                        warn!(
                            "anisette: upstream panicked on attempt {}/{} (state preserved): {}",
                            attempt + 1,
                            MAX_RETRIES,
                            msg
                        );
                        last_err = Some(AnisetteError::InvalidArgument(format!(
                            "Anisette call panicked (attempt {}/{}): {}",
                            attempt + 1,
                            MAX_RETRIES,
                            msg
                        )));
                    }
                    Err(_) => {
                        // Timeout — the provider's infinite-loop bug on WS drop.
                        // Network-layer issue, not state corruption.
                        warn!(
                            "anisette: upstream timed out on attempt {}/{} (state preserved, likely infinite loop on WS drop)",
                            attempt + 1,
                            MAX_RETRIES,
                        );
                        last_err = Some(AnisetteError::InvalidArgument(
                            format!(
                                "Anisette timed out (attempt {}/{}). \
                                 The Mac relay or anisette server may be unreachable.",
                                attempt + 1,
                                MAX_RETRIES,
                            ),
                        ));
                    }
                }
            }

            error!(
                "anisette: all {} attempts exhausted in {:?} — returning error to caller (lock will be released)",
                MAX_RETRIES,
                call_start.elapsed()
            );
            Err(last_err.unwrap_or_else(|| {
                AnisetteError::InvalidArgument("Anisette provisioning failed".into())
            }))
        }
    }
}
