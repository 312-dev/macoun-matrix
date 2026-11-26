# iMessage effects

macoun bridges iMessage bubble and screen effects in both directions. The
Matrix side uses one content key on `m.room.message`, the same key and value
format as Beeper's iMessage bridge:

```
"com.beeper.imessage.effect": "<Apple effect id>"
```

The value is Apple's effect id verbatim (`expressiveSendStyleId`, carried as
`iid` in the iMessage payload). Ids outside the table below are dropped in both
directions.

## Effect ids

| Kind | Name in Messages | Effect id |
|---|---|---|
| Bubble | Slam | `com.apple.MobileSMS.expressivesend.impact` |
| Bubble | Loud | `com.apple.MobileSMS.expressivesend.loud` |
| Bubble | Gentle | `com.apple.MobileSMS.expressivesend.gentle` |
| Bubble | Invisible Ink | `com.apple.MobileSMS.expressivesend.invisibleink` |
| Screen | Echo | `com.apple.messages.effect.CKEchoEffect` |
| Screen | Spotlight | `com.apple.messages.effect.CKSpotlightEffect` |
| Screen | Balloons | `com.apple.messages.effect.CKHappyBirthdayEffect` |
| Screen | Confetti | `com.apple.messages.effect.CKConfettiEffect` |
| Screen | Love | `com.apple.messages.effect.CKHeartEffect` |
| Screen | Lasers | `com.apple.messages.effect.CKLasersEffect` |
| Screen | Fireworks | `com.apple.messages.effect.CKFireworksEffect` |
| Screen | Celebration | `com.apple.messages.effect.CKSparklesEffect` |
| Screen | Shooting Star | `com.apple.messages.effect.CKShootingStarEffect` |

Balloons is `CKHappyBirthdayEffect` and Celebration is `CKSparklesEffect`;
there is no `CKBalloonEffect`. The screen effects are the bundles in
`/System/Library/Messages/iMessageEffects` on iOS 18 and 26. The list lives in
`imessageEffects` in `pkg/connector/macoun_effects.go`.

## iMessage to Matrix

Every event the message becomes (the text, each attachment) carries the key.
`body` is the text as sent: Apple sends no "(sent with Slam)" text, and the
bridge adds none.

```json
{
  "msgtype": "m.text",
  "body": "happy birthday",
  "com.beeper.imessage.effect": "com.apple.messages.effect.CKHappyBirthdayEffect"
}
```

Invisible ink uses the same key. Its text events also get a Matrix spoiler in
`formatted_body`, so a client that ignores the key still hides the text.
`body` stays plain, which means a notification built from `body` shows the
text; a client that wants to hide it in notifications checks the key.

```json
{
  "msgtype": "m.text",
  "body": "the code is 1234",
  "format": "org.matrix.custom.html",
  "formatted_body": "<span data-mx-spoiler>the code is 1234</span>",
  "com.beeper.imessage.effect": "com.apple.MobileSMS.expressivesend.invisibleink"
}
```

A text event longer than 8 KiB keeps the key but gets no spoiler, to keep the
event under the homeserver's size limit. Attachments with invisible ink carry
the key only. Messages imported from iCloud (backfill) carry no effect,
because the bridge reads none from CloudKit.

## Matrix to iMessage

Put the key on an outgoing text message (`m.text`, `m.notice`, `m.emote`) or
attachment (`m.image`, `m.video`, `m.audio`, `m.file`) with an id from the
table. `body` is sent unchanged.

```json
{
  "msgtype": "m.text",
  "body": "we did it",
  "com.beeper.imessage.effect": "com.apple.messages.effect.CKConfettiEffect"
}
```

- Invisible ink is the same key with the invisible ink id. A spoiler in
  `formatted_body` alone is not turned into invisible ink.
- An unknown id, or a value that is not a string, is logged and the message
  goes out without an effect.
- On an attachment with a caption (a `body` that differs from `filename`),
  the caption goes out as its own iMessage after the attachment, and the
  effect rides on the attachment only, so it plays once. A separate `m.text`
  sent after the attachment is an ordinary message: put the key on it only if
  it should have its own effect.
- A message bound for the text-message relay (SMS) goes out without the
  effect, which SMS cannot carry.
- A text long enough to be split carries the effect on its first part only.

Inside the bridge the Go connector passes the effect to `send_message` in
`pkg/rustpushgo/src/lib.rs` as an envelope ahead of the text,
`\x00FX\x01<effect id>\x00`, next to the existing rich-link envelope. Rust
strips it and sets the message's effect. For an attachment the envelope alone
is `send_attachment`'s `body` argument, split off the same way
(`attachment_effect_and_subject`).
