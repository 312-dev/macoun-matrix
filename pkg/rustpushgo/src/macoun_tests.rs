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
