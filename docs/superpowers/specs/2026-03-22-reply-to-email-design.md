# Reply-to-Email Feature Design

## Overview

Add bidirectional email capability: when a user replies to a forwarded Telegram message, the bot sends that reply as an email back to the original sender and all participants.

## Architecture

### Forward path (existing, with modifications)

1. Email arrives via SMTP, is parsed and formatted
2. Forwarded message now includes `Reply-To:` and `CC:` headers in the template (when present)
3. Message is sent to Telegram with `ForceReply` markup, prompting the user to reply
4. Attachments follow as replies to the main message (unchanged)

### Reply path (new)

1. A long-polling goroutine calls Telegram `getUpdates` on startup
2. When a reply to a bot message is received:
   - Parse the original (replied-to) message text to extract `From:`, `To:`, `Reply-To:`, `CC:`, `Subject:`
   - If parsing fails → notify user with error message
   - If outbound SMTP is not configured → notify user that reply feature is not configured
3. Compose and send email:
   - **From:** the original `To:` address (the person whose inbox received the email)
   - **To:** original `Reply-To:` if present, otherwise original `From:`
   - **CC:** all original `To:` and `CC:` addresses, minus the "From" address used above (avoid sending to yourself)
   - **Subject:** `Re: <original subject>` (skip prefix if already present)
   - **Body:** the Telegram reply text (plain text)
4. On success → send confirmation message to the user in Telegram
5. On failure → send error message to the user in Telegram

### Multiple recipients in `To:` field

When the original email was sent to multiple chat IDs, the same email appears in multiple chats. Each chat user can reply independently. The `To:` field may contain multiple addresses — the bot uses the first one as the "From" for the reply. If a future need arises to map chat IDs to specific email addresses, that can be added later.

**Refinement:** If there's only one address in `To:`, use it. If there are multiple, ideally we'd know which one maps to this chat — but since we don't have that mapping, use the first address. This is a known limitation.

## Template changes

### New placeholders

- `{reply_to}` — the `Reply-To` header value (if present)
- `{cc}` — the `CC` header value (if present)

### Default template update

Old:
```
From: {from}\nTo: {to}\nSubject: {subject}\n\n{body}\n\n{attachments_details}
```

New:
```
From: {from}\nTo: {to}\nCC: {cc}\nReply-To: {reply_to}\nSubject: {subject}\n\n{body}\n\n{attachments_details}
```

Lines with empty values (`CC:` when no CC, `Reply-To:` when no Reply-To) are omitted from the final message to keep it clean.

## Parsing reply metadata from Telegram message

When the bot receives a reply, it reads `reply_to_message.text` and extracts headers using simple prefix matching:

- `From: ` → original sender
- `To: ` → original recipient(s)
- `CC: ` → CC'd addresses
- `Reply-To: ` → reply-to address
- `Subject: ` → original subject

This works with the default template. Users with custom templates that omit these fields won't be able to use the reply feature — that's acceptable and documented.

## Configuration

### New environment variables / CLI flags

| Env Variable | CLI Flag | Default | Purpose |
|---|---|---|---|
| `ST_SMTP_OUT_HOST` | `--smtp-out-host` | (none) | Outbound SMTP server host |
| `ST_SMTP_OUT_PORT` | `--smtp-out-port` | `587` | Outbound SMTP server port |
| `ST_SMTP_OUT_USERNAME` | `--smtp-out-username` | (none) | SMTP auth username |
| `ST_SMTP_OUT_PASSWORD` | `--smtp-out-password` | (none) | SMTP auth password |

### YAML config file support

```yaml
smtp_out:
  host: smtp.example.com
  port: 587
  username: user@example.com
  password: secret
```

Config file values are overridden by environment variables / CLI flags (consistent with existing behavior).

### Feature activation

- Reply feature is **enabled** when `ST_SMTP_OUT_HOST` (or `smtp_out.host` in config) is set
- If a user replies to a forwarded message but the feature is not configured, the bot responds: "Reply-to-email is not configured. Set ST_SMTP_OUT_HOST to enable."

## ForceReply markup

When sending forwarded email messages to Telegram, include `reply_markup` with Telegram's `ForceReply` object:

```json
{"force_reply": true, "selective": true}
```

This causes the Telegram client to automatically show the reply interface when the user views the message, making it convenient to reply.

## User notifications

| Scenario | Bot response |
|---|---|
| Email sent successfully | "Email sent to `<recipients>`" |
| Outbound SMTP not configured | "Reply-to-email is not configured. Set ST_SMTP_OUT_HOST to enable." |
| Failed to parse original message | "Could not parse the original email from the message." |
| SMTP send failed | "Failed to send email: `<error>`" |
| Reply to a non-bot message | "I can only reply to messages that I forwarded." |

All notifications are sent as replies to the user's message in the same chat.

## Telegram Bot polling

- Uses `getUpdates` with long polling (`timeout=30`)
- Tracks `offset` to acknowledge processed updates
- Runs in a dedicated goroutine, started alongside the SMTP server
- Respects graceful shutdown (context cancellation)
- Only processes updates containing `message.reply_to_message` where `reply_to_message.from.id` matches the bot's own ID

### Bot identity

To know which messages are "ours", the bot calls `getMe` once on startup to retrieve its own user ID.

## Dependencies

- `gopkg.in/gomail.v2` — already in go.mod (used in tests). Will be used for composing and sending outbound emails via SMTP.
- No new dependencies needed.

## Error handling

- Outbound SMTP connection failures → notify user, log error
- Invalid/unparseable reply-to message → notify user
- Telegram API polling errors → log, retry with backoff
- Graceful shutdown → stop polling loop, cancel in-flight sends

## Testing

- Unit tests for parsing email metadata from Telegram message text
- Unit tests for composing reply emails (From/To/CC/Subject logic)
- Integration-style tests using a mock SMTP server and mock Telegram API
- Test ForceReply markup is included in forwarded messages
- Test notification messages for all scenarios (success, not configured, parse failure, send failure)

## README updates

- Document the new reply feature in a new section
- Document new configuration options (env vars, CLI flags, YAML config)
- Update the default template example
- Note the limitation: custom templates must include `From:`, `To:`, `Subject:` lines for reply to work
