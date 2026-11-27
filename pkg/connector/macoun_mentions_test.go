package connector

import (
	"reflect"
	"testing"

	"maunium.net/go/mautrix/id"
)

func testResolve(uri string) id.UserID {
	switch uri {
	case "tel:+15555550100":
		return "@me:example.org"
	case "tel:+15555550101":
		return "@imessage_tel=3a+15555550101:example.org"
	}
	return ""
}

func TestRewriteMentionHTML(t *testing.T) {
	// The shape rustpush's parts_to_html emits for "Friday, @Me" and
	// "after @Sam and I's".
	src := `See you Friday, <a href="tel:+15555550100">@Me</a>` + "\n" +
		`after <a href="tel:+15555550101">@Sam</a> and I's`
	got, users := rewriteMentionHTML(src, testResolve)
	want := `See you Friday, <a href="https://matrix.to/#/@me:example.org">Me</a><br/>` +
		`after <a href="https://matrix.to/#/@imessage_tel=3a+15555550101:example.org">Sam</a> and I's`
	if got != want {
		t.Fatalf("formatted body\n got %q\nwant %q", got, want)
	}
	wantUsers := []id.UserID{"@me:example.org", "@imessage_tel=3a+15555550101:example.org"}
	if !reflect.DeepEqual(users, wantUsers) {
		t.Fatalf("mentions %v, want %v", users, wantUsers)
	}
}

func TestRewriteMentionHTMLUnresolvedAndRepeated(t *testing.T) {
	src := `<a href="tel:+15555550101">@Sam</a>, <a href="tel:+15555550101">@Sam</a> and <a href="mailto:x@example.com">@X &amp; Co</a>`
	got, users := rewriteMentionHTML(src, testResolve)
	want := `<a href="https://matrix.to/#/@imessage_tel=3a+15555550101:example.org">Sam</a>, ` +
		`<a href="https://matrix.to/#/@imessage_tel=3a+15555550101:example.org">Sam</a> and @X &amp; Co`
	if got != want {
		t.Fatalf("formatted body\n got %q\nwant %q", got, want)
	}
	if len(users) != 1 {
		t.Fatalf("a repeated mention must be listed once, got %v", users)
	}
}
