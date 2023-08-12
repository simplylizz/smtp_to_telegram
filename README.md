# SMTP to Telegram

[![Docker Hub](https://img.shields.io/docker/pulls/simplylizz/smtp_to_telegram.svg?style=flat-square)][Docker Hub]
[![Go Report Card](https://goreportcard.com/badge/github.com/simplylizz/smtp_to_telegram?style=flat-square)][Go Report Card]
[![License](https://img.shields.io/github/license/simplylizz/smtp_to_telegram.svg?style=flat-square)][License]

[Docker Hub]:      https://hub.docker.com/r/simplylizz/smtp_to_telegram
[Go Report Card]:  https://goreportcard.com/report/github.com/simplylizz/smtp_to_telegram
[License]:         https://github.com/simplylizz/smtp_to_telegram/blob/main/LICENSE

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

The default Telegram message format is:

```
From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}
```

A custom format might be specified as well:

```
ST_TELEGRAM_CHAT_IDS=<CHAT_ID1>,<CHAT_ID2>
ST_TELEGRAM_BOT_TOKEN=<BOT_TOKEN>
ST_TELEGRAM_MESSAGE_TEMPLATE="Subject: {subject}\\n\\n{body}"
ST_SMTP_ALLOWED_HOSTS=cvzilla.net,example.com
```
