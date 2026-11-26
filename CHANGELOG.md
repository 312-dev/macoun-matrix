# Unreleased

* Added universal macOS binaries of `nac-relay` and `extract-key` to releases
  (`macoun-mac-tools-<tag>.tar.gz`). The bridge stays source only.
* Added bubble and screen effects in both directions, through the
  `com.beeper.imessage.effect` content key. Outbound effects are sent on text
  messages only. See [docs/effects.md](docs/effects.md).
* Fixed members of an iMessage group who never post not appearing in the
  Matrix room. Every live group message now joins the members on Apple's
  roster.
* Fixed group ghosts being removed from a room whose stored roster was unknown.
* Fixed starting a chat through the provisioning API with a bare phone number
  or email (no `tel:` or `mailto:`) failing with IDS error 6001.

# v0.1.0 (2026-11-26)

First public release, forked from the corten-matrix source archive of
2026-11-01. The [release notes](https://github.com/312-dev/macoun-matrix/releases/tag/v0.1.0)
list what differs from that snapshot.
