# Reply-to-Email Feature Design

## Overview

Add bidirectional email capability: when a user replies to a forwarded Telegram message, the bot sends that reply as an email back to the original sender and all participants.

## Architecture

### Forward path (existing, with modifications)

1. Email arrives via SMTP, is parsed and formatted
2. Forwarded message now includes `Reply-To:` and `CC:` headers in the template (when present)
3. **If outbound SMTP is configured:** message is sent with `ForceReply` markup, prompting the user to reply
4. Attachments follow as replies to the main message (unchanged)

### Reply path (new)

1. A long-polling goroutine calls Telegram `getUpdates` on startup (only when outbound SMTP is configured)
2. When a reply to a bot message is received:
   - Parse the original (replied-to) message text to extract `From:`, `To:`, `Reply-To:`, `CC:`, `Subject:`
   - If parsing fails → notify user with error message
   - If outbound SMTP is not configured → notify user that reply feature is not configured
3. Compose and send email:
   - **From:** the original `To:` address (the person whose inbox received the email)
   - **To:** original `Reply-To:` if present, otherwise original `From:`
   - **CC:** all original `To:` and `CC:` addresses, minus the "From" address used above (avoid sending to yourself)
   - **Subject:** `Re: <original subject>` (skip prefix if already present)
   - **Body:** the Telegram reply text, plain text (`Content-Type: text/plain`)
4. On success → send confirmation message: "Email sent from `<from>` to `<recipients>`"
5. On failure → send error message to the user in Telegram
6. Replies to non-bot messages → silently ignored

### Multiple recipients in `To:` field

When the original email was sent to multiple chat IDs, the same email appears in multiple chats. Each chat user can reply independently. The `To:` field may contain multiple addresses — the bot uses the first one as the "From" for the reply. This is a known limitation documented in README.

## Breaking change: remove custom message templates

The `ST_TELEGRAM_MESSAGE_TEMPLATE` / `--message-template` option is removed. The message format is now fixed and controlled by the application to ensure reliable parsing for the reply feature.

This is a **major version bump** (breaking change). Users on the current version who rely on custom templates can stay on the previous major version.

If `ST_TELEGRAM_MESSAGE_TEMPLATE` is set, the application exits with an error at startup explaining that custom templates are no longer supported. This guard can be removed in 3.0.0 or later.

## Message format

The fixed message format is:

```
From: {from}
To: {to}
CC: {cc}
Reply-To: {reply_to}
Subject: {subject}

{body}

{attachments_details}
```

Lines with empty values (`CC:` when no CC, `Reply-To:` when no Reply-To) are omitted. Implementation: after placeholder replacement, strip lines matching `^(CC|Reply-To): \s*$`.

## Parsing reply metadata from Telegram message

When the bot receives a reply, it reads `reply_to_message.text` and extracts headers by scanning lines **before the first blank line** (the "header section", mirroring email format):

- `From: ` → original sender
- `To: ` → original recipient(s)
- `CC: ` → CC'd addresses
- `Reply-To: ` → reply-to address
- `Subject: ` → original subject

Only lines in the header section are matched, so body content starting with `Subject: ` won't cause mis-parsing.

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

### New struct

A new `SMTPOutConfig` struct holds outbound SMTP settings, separate from the existing `SMTPConfig` (which is for the inbound listener).

### Feature activation

- Reply feature (polling + ForceReply) is **enabled** when `ST_SMTP_OUT_HOST` (or `smtp_out.host` in config) is set
- If a user replies to a forwarded message but the feature is not configured, the bot responds: "Reply-to-email is not configured. Set ST_SMTP_OUT_HOST to enable."

## ForceReply markup

**Only when outbound SMTP is configured**, forwarded email messages include `reply_markup` with Telegram's `ForceReply` object:

```json
{"force_reply": true, "selective": true}
```

When SMTP outbound is not configured, messages are sent without ForceReply (preserving current behavior).

## TLS behavior

Port 587 (default): STARTTLS is used (standard for submission port). Port 465: implicit TLS. Port 25: plain connection. This follows `gomail.v2`'s default behavior. No additional TLS configuration is exposed — the library handles it based on port number.

## User notifications

| Scenario | Bot response |
|---|---|
| Email sent successfully | "Email sent from `<from>` to `<recipients>`" |
| Outbound SMTP not configured | "Reply-to-email is not configured. Set ST_SMTP_OUT_HOST to enable." |
| Failed to parse original message | "Could not parse the original email from the message." |
| SMTP send failed | "Failed to send email: `<error>`" |

Replies to non-bot messages are silently ignored.

All notifications are sent as replies to the user's message in the same chat.

## Telegram Bot polling

- Uses `getUpdates` with long polling (`timeout=30`)
- **Dedicated HTTP client** with timeout of 40 seconds (long-poll timeout + 10s buffer) to avoid race between HTTP timeout and Telegram's response
- Tracks `offset` to acknowledge processed updates
- On cold start, processes and discards any stale pending updates before entering the main loop
- Runs in a dedicated goroutine, started alongside the SMTP server
- Respects graceful shutdown: uses a cancellable context, cancelled in `awaitShutdown` before `d.Shutdown()`
- Only processes updates containing `message.reply_to_message` where `reply_to_message.from.id` matches the bot's own ID

### Bot identity

To know which messages are "ours", the bot calls `getMe` once on startup to retrieve its own user ID.

## Code organization

New reply-related code goes into a separate file (`reply.go`) to keep the main file manageable. This includes:
- `SMTPOutConfig` struct
- Polling loop
- Message parsing
- Email composition and sending
- Notification helpers

## Dependencies

- `gopkg.in/gomail.v2` — already in go.mod (used in tests). Will be used for composing and sending outbound emails via SMTP. The library is old but stable and sufficient for plain-text email sending.
- No new dependencies needed.

## FormattedEmail struct changes

Add `ReplyTo` and `CC` fields to the existing `FormattedEmail` struct. These are populated from the parsed email's headers and threaded through to `FormatMessage` for template replacement.

## Error handling

- Outbound SMTP connection failures → notify user, log error
- Invalid/unparseable reply-to message → notify user
- Telegram API polling errors → log, retry with backoff
- Graceful shutdown → stop polling loop via context cancellation, then shut down SMTP daemon

## Testing

- Unit tests for parsing email metadata from Telegram message text (header section only)
- Unit tests for composing reply emails (From/To/CC/Subject logic)
- Unit tests for empty-line omission in template formatting
- Integration-style tests using a mock SMTP server and mock Telegram API
- Test ForceReply markup is included only when outbound SMTP is configured
- Test notification messages for all scenarios (success, not configured, parse failure, send failure)

## README updates

- Document the new reply feature in a new section
- Document new configuration options (env vars, CLI flags, YAML config)
- Update the default template example
- Note limitations: custom templates must include `From:`, `To:`, `Subject:` lines for reply to work; multiple `To:` addresses use the first one as sender
