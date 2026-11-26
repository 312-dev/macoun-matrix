// macoun-matrix: tests for macoun patches to the pinned rustpush tree, run
// from this crate because rustpush's own test target needs certificates that
// are not in the public source.
// SPDX-License-Identifier: MPL-2.0

use rustpush::ids::user::{macoun_rereg_in_s, MACOUN_REREG_LEAD_S};

const DAY: i64 = 86400;

#[test]
fn macoun_rereg_seven_days_before_expiry() {
    assert_eq!(MACOUN_REREG_LEAD_S, 7 * DAY);
    // 45-day certificate, registered just now: rereg after 38 days.
    assert_eq!(macoun_rereg_in_s(45 * DAY, 0), 38 * DAY);
    // Under 7 days left on an old registration: rereg now.
    assert!(macoun_rereg_in_s(3 * DAY, 40 * DAY) <= 0);
}

#[test]
fn macoun_rereg_fresh_registration_is_not_repeated() {
    // A new certificate with only 5 days left waits out the one-day minimum age.
    assert_eq!(macoun_rereg_in_s(5 * DAY, 60), DAY - 60);
}

#[test]
fn macoun_rereg_never_later_than_upstream() {
    // Expires in an hour, registered 10 minutes ago: upstream's -300 s wins.
    assert_eq!(macoun_rereg_in_s(3600, 600), 3300);
}

#[test]
fn macoun_provider_outage_mask_tracks_latest_result_per_kind() {
    use crate::{provider_outage_mask, record_provider_result, PROVIDER_ANISETTE, PROVIDER_VALIDATION_DATA};
    assert_eq!(provider_outage_mask(), 0);
    record_provider_result(PROVIDER_ANISETTE, false);
    record_provider_result(PROVIDER_ANISETTE, false);
    assert_eq!(provider_outage_mask(), PROVIDER_ANISETTE);
    record_provider_result(PROVIDER_VALIDATION_DATA, false);
    assert_eq!(provider_outage_mask(), PROVIDER_ANISETTE | PROVIDER_VALIDATION_DATA);
    // One kind recovering does not clear the other.
    record_provider_result(PROVIDER_ANISETTE, true);
    assert_eq!(provider_outage_mask(), PROVIDER_VALIDATION_DATA);
    record_provider_result(PROVIDER_VALIDATION_DATA, true);
    assert_eq!(provider_outage_mask(), 0);
}

#[test]
fn macoun_effect_envelope_is_split_off() {
    use crate::split_effect_envelope;
    let (effect, rest) = split_effect_envelope("\u{0}FX\u{1}com.apple.messages.effect.CKConfettiEffect\u{0}hello".to_string());
    assert_eq!(effect.as_deref(), Some("com.apple.messages.effect.CKConfettiEffect"));
    assert_eq!(rest, "hello");
    // A rich-link envelope after it is left for the rich-link parser.
    let (effect, rest) = split_effect_envelope("\u{0}FX\u{1}com.apple.MobileSMS.expressivesend.impact\u{0}\u{0}RL\u{1}a\u{1}b\u{1}c\u{1}\u{0}body".to_string());
    assert_eq!(effect.as_deref(), Some("com.apple.MobileSMS.expressivesend.impact"));
    assert_eq!(rest, "\u{0}RL\u{1}a\u{1}b\u{1}c\u{1}\u{0}body");
}

#[test]
fn macoun_effect_envelope_absent_or_malformed() {
    use crate::split_effect_envelope;
    for text in ["hello", "\u{0}FX\u{1}no terminator", "\u{0}FX\u{1}\u{0}empty id", "\u{0}RL\u{1}a\u{0}b"] {
        let (effect, rest) = split_effect_envelope(text.to_string());
        assert_eq!(effect, None, "{text:?}");
        assert_eq!(rest, text);
    }
}
