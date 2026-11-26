package connector

import "testing"

// Clients' start-chat flows send identifiers to the provisioning API without a
// URI scheme. IDS rejects those with 6001, so ResolveIdentifier must turn them
// into the tel:/mailto: form before the lookup.
func TestNormalizeStartChatIdentifierAddsScheme(t *testing.T) {
	cases := map[string]string{
		"+14045551234":        "tel:+14045551234",
		"+1 (404) 555-1234":   "tel:+14045551234",
		"tel:+14045551234":    "tel:+14045551234",
		"Someone@Example.com": "mailto:someone@example.com",
		"mailto:a@b.com":      "mailto:a@b.com",
	}
	for in, want := range cases {
		if got := normalizeStartChatIdentifier(in); got != want {
			t.Errorf("normalizeStartChatIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}
