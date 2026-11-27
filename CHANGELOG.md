# Unreleased

* Added universal macOS binaries of `nac-relay` and `extract-key` to releases
  (`macoun-mac-tools-<tag>.tar.gz`). The bridge stays source only.
* Added bubble and screen effects in both directions, through the
  `com.beeper.imessage.effect` content key, on text and attachments. With a
  caption the effect rides on the attachment and the caption follows plain.
  Not on SMS. See [docs/effects.md](docs/effects.md).
* Fixed members of an iMessage group who never post not appearing in the
  Matrix room. Every live group message now joins the members on Apple's
  roster.
* Fixed an iMessage group created before its members were known staying named
  "Group Chat". Unnamed groups are named after their members, locally only,
  and the name follows roster and contact changes.
* Fixed group ghosts being removed from a room whose stored roster was unknown.
* Fixed starting a chat through the provisioning API with a bare phone number
  or email (no `tel:` or `mailto:`) failing with IDS error 6001.
* Fixed a contact's avatar being set again in every shared room whenever an
  external CardDAV server (seen with Google) served the same photo in a
  different encoding.

# v0.1.0 (2026-11-26)

First public release, forked from the corten-matrix source archive of
2026-11-01. The [release notes](https://github.com/312-dev/macoun-matrix/releases/tag/v0.1.0)
list what differs from that snapshot.
