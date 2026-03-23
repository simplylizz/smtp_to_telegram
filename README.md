# SMTP to Telegram

[![Docker Hub](https://img.shields.io/docker/pulls/simplylizz/smtp_to_telegram.svg?style=flat-square)][Docker Hub]
[![Go Report Card](https://goreportcard.com/badge/github.com/simplylizz/smtp_to_telegram?style=flat-square)][Go Report Card]
[![License](https://img.shields.io/github/license/simplylizz/smtp_to_telegram.svg?style=flat-square)][License]

[Docker Hub]:      https://hub.docker.com/r/simplylizz/smtp_to_telegram
[Go Report Card]:  https://goreportcard.com/report/github.com/simplylizz/smtp_to_telegram
[License]:         https://github.com/simplylizz/smtp_to_telegram/blob/main/LICENSE

Forked from [KostyaEsmukov/smtp_to_telegram](https://github.com/KostyaEsmukov/smtp_to_telegram) package.

`smtp_to_telegram` is a small program which listens for SMTP and sends
all incoming Email messages to Telegram. It also supports bidirectional
communication: you can reply to a forwarded Telegram message to send an
email back to the original sender.

Say you have a software which can send Email notifications via SMTP.
You may use `smtp_to_telegram` as an SMTP server so
the notification mail would be sent to the chosen Telegram chats.

## Getting started

1. Create a new Telegram bot: https://core.telegram.org/bots#creating-a-new-bot.
2. Open that bot account in the Telegram account which should receive
   the messages, press `/start`.
3. Retrieve a chat id with `curl https://api.telegram.org/bot<BOT_TOKEN>/getUpdates`.
   If you don't see chat id, try writing one more message to the bot.
4. Repeat steps 2 and 3 for each Telegram account which should receive the messages.
5. Create `env_file` from `env_file.example` and fill it with your data.
6. Start a docker container:

```
docker compose up
```

You may use `localhost:25` as the target SMTP address.
No TLS or authentication is required.

The Telegram message format is:

```
From: {from}
To: {to}
CC: {cc}
Reply-To: {reply_to}
Subject: {subject}

{body}

{attachments_details}
```

The `CC` and `Reply-To` lines are only shown when present. Custom message
templates are no longer supported (breaking change in v2).

## Reply to Email

When an email is forwarded to Telegram, the bot uses Telegram's ForceReply
feature to prompt you to reply. If you reply to that message in Telegram,
the bot will send your reply as an email back to the original sender.

### How it works

1. An incoming email is forwarded to your Telegram chat.
2. The bot sends the message with ForceReply enabled, prompting you to reply.
3. You write a reply in Telegram.
4. The bot sends your reply as an email to the original sender's address.

### Configuration

To enable the reply feature, configure outbound SMTP via environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `ST_SMTP_OUT_HOST` | Outbound SMTP server hostname (required to enable replies) | — |
| `ST_SMTP_OUT_PORT` | Outbound SMTP server port | `587` |
| `ST_SMTP_OUT_USERNAME` | SMTP authentication username | — |
| `ST_SMTP_OUT_PASSWORD` | SMTP authentication password | — |

Or via the YAML config file (see [Configuration File](#configuration-file)):

```yaml
smtp_out:
  host: smtp.example.com
  port: 587
  username: user@example.com
  password: secret
```

### Limitations

- If the original email had multiple `To:` addresses, the first address is
  used as the sender address for the reply.

## Development

Install [pre-commit](https://pre-commit.com/) hooks to run formatting,
linting, and tests before each commit:

```
pre-commit install
```

## Configuration File

You can define filter rules and outbound SMTP settings in a YAML configuration
file. Rules match against email fields (from, to, subject, body, html) using
regex patterns and reject emails that match.

### Setup

Configure the config file path using the environment variable:
```
ST_CONFIG_FILE=/path/to/config.yaml
```

Or use the command line flag:
```
--config-file /path/to/config.yaml
```

### Example Configuration

```yaml
smtp_out:
  host: smtp.example.com
  port: 587
  username: user@example.com
  password: secret

filter_rules:
  # Simple rule: single condition
  - name: block-adnxs-tracking
    conditions:
      - field: body
        pattern: 'adnxs\.com'

  # AND logic: ALL conditions must match (default)
  - name: block-dating-spam
    match: all
    conditions:
      - field: from
        pattern: '@ecinetworks\.com$'
      - field: subject
        pattern: 'get(ting)? to know'

  # OR logic: ANY condition matches
  - name: block-spam-domains
    match: any
    conditions:
      - field: body
        pattern: 'cdnex\.online'
      - field: body
        pattern: 'spam-tracker\.net'
      - field: body
        pattern: 'click-now\.xyz'

  # Match URLs in HTML-only emails
  - name: block-html-tracking
    conditions:
      - field: body_or_html
        pattern: 'https?://[^\s]+\.(xyz|top|click)'

```

### Rule Structure

| Field | Description |
|-------|-------------|
| `name` | Rule identifier (used in logs) |
| `match` | `all` (default) - all conditions must match; `any` - at least one condition must match |
| `conditions` | List of conditions to evaluate |

### Available Fields

| Field | Description |
|-------|-------------|
| `from` | Sender email address |
| `to` | Recipient email address |
| `subject` | Email subject line |
| `body` | Plain text body |
| `html` | HTML body |
| `body_or_html` | Matches if pattern found in either body OR html (recommended for URL matching) |

### How It Works

1. Rules are loaded and regex patterns are compiled at startup (invalid patterns or field names cause startup failure)
2. All patterns are **case-insensitive**
3. After an email is parsed, each rule is evaluated in order
4. First matching rule rejects the email with a 554 SMTP error
5. If no rules match, the email is forwarded to Telegram
