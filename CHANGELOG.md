# Changelog

Notable changes in each release. Dependency updates are not listed.

## 2.2.0 - 2026-11-11

### Added

- Spambox: emails rejected by filter rules can be kept in a JSON Lines file
  for reviewing and tuning the rules. Off by default; set `ST_SPAMBOX_FILE`
  (and optionally `ST_SPAMBOX_MAX_SIZE`). See "Spambox" in the README.
- The log line for a rejected email now includes the sender and the subject.

### Fixed

- Line breaks in decoded `Subject`, `Cc` and `Reply-To` values no longer add
  fake header lines to the forwarded message. The reply feature reads those
  lines to choose the recipients.
- Replying from Telegram failed when the email's `Reply-To` listed several
  addresses. The reply now goes to each of them.
- SMTP error replies are kept on one line, so the sender gets the whole error
  text.

## 2.1.1 - 2026-10-19

### Changed

- Docker images are cross-compiled instead of built under emulation.

## 2.1.0 - 2026-09-29

### Added

- `--log-level` flag (`ST_LOG_LEVEL`), `info` by default.
- Replies sent from Telegram are threaded under the original email
  (`In-Reply-To` and `References` are set from its `Message-ID`).

### Changed

- The sender shown in Telegram and matched by `from` filter rules is the
  `From:` header address. The SMTP envelope sender is used if the header is
  missing, and `from` rules still match it too.
- When Telegram is unavailable the sender gets a temporary 421 error instead
  of a permanent 554, so the email is retried instead of dropped.
- An email may take up to 10 minutes to be sent to Telegram before the SMTP
  transaction times out (was 30 seconds).
- Built with Go 1.27.

## 2.0.0 - 2026-04-06

### Added

- Reply to emails from Telegram: reply to a forwarded message and the bot
  sends your text as an email to the original sender. Needs an outbound SMTP
  server (`ST_SMTP_OUT_*` or `smtp_out` in the config file).

### Changed

- **Breaking:** custom message templates (`ST_TELEGRAM_MESSAGE_TEMPLATE`) are
  no longer supported. The message format is fixed, because the reply feature
  parses it.
- Releases are built with GitHub Actions instead of GoReleaser.
