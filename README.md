# SMTP to Telegram

[![Docker Hub](https://img.shields.io/docker/pulls/simplylizz/smtp_to_telegram.svg?style=flat-square)][Docker Hub]
[![Go Report Card](https://goreportcard.com/badge/github.com/simplylizz/smtp_to_telegram?style=flat-square)][Go Report Card]
[![License](https://img.shields.io/github/license/simplylizz/smtp_to_telegram.svg?style=flat-square)][License]

[Docker Hub]:      https://hub.docker.com/r/simplylizz/smtp_to_telegram
[Go Report Card]:  https://goreportcard.com/report/github.com/simplylizz/smtp_to_telegram
[License]:         https://github.com/simplylizz/smtp_to_telegram/blob/main/LICENSE

Forked from [KostyaEsmukov/smtp_to_telegram](https://github.com/KostyaEsmukov/smtp_to_telegram) package.

`smtp_to_telegram` is a small program which listens for SMTP and sends
all incoming Email messages to Telegram.

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

## How to use

Request to check the service:
```
curl \
  --url 'smtp://localhost:25' \
  --mail-from sender@test.test \
  --mail-rcpt user@test.test --mail-rcpt user1@test.test \
  -H "Subject: Test smtp_to_telegram" -F '=(;type=multipart/mixed' -F "=This message came via smtp;type=text/plain" -F '=)'
```

Sending personal messages is supported. Instead of email, enter an entry like `000000000@telegram.org`. Where `000000000` is `chat id`.
Classic emails will still be sent to the `ID` specified in `ST_TELEGRAM_CHAT_IDS`.

## Options

A custom format might be specified as well:
```
ST_TELEGRAM_BOT_TOKEN=<BOT_TOKEN>
ST_TELEGRAM_CHAT_IDS=<CHAT_ID1>,<CHAT_ID2> # optional
ST_TELEGRAM_MESSAGE_TEMPLATE="Subject: {subject}\\n\\n{body}" # optional
ST_SMTP_ALLOWED_HOSTS=cvzilla.net,example.com # optional
```

The default Telegram message format is:
```
From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}
```
