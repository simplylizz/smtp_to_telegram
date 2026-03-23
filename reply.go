package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/gomail.v2"
)

// Telegram update types for processing replies.

type TelegramUpdate struct {
	UpdateID int                    `json:"update_id"`
	Message  *TelegramUpdateMessage `json:"message"`
}

type TelegramUpdateMessage struct {
	MessageID      int                   `json:"message_id"`
	Chat           TelegramChat          `json:"chat"`
	Text           string                `json:"text"`
	From           *TelegramUser         `json:"from"`
	ReplyToMessage *TelegramReplyMessage `json:"reply_to_message"`
}

type TelegramReplyMessage struct {
	MessageID int           `json:"message_id"`
	From      *TelegramUser `json:"from"`
	Text      string        `json:"text"`
}

type TelegramChat struct {
	ID int64 `json:"id"`
}

type TelegramUser struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

type TelegramGetUpdatesResult struct {
	Ok     bool             `json:"ok"`
	Result []TelegramUpdate `json:"result"`
}

type TelegramGetMeResult struct {
	Ok     bool          `json:"ok"`
	Result *TelegramUser `json:"result"`
}

// SMTPOutConfig holds configuration for outbound SMTP (reply-to-email feature).
type SMTPOutConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

func (c *SMTPOutConfig) IsConfigured() bool {
	return c.Host != ""
}

// ParsedHeaders contains the parsed header fields from a Telegram message.
type ParsedHeaders struct {
	From    string
	To      string
	CC      string
	ReplyTo string
	Subject string
}

var errMissingFromHeader = errors.New("missing From header in message")

// ParseMessageHeaders extracts email-style headers from the top of a Telegram message.
func ParseMessageHeaders(text string) (ParsedHeaders, error) {
	var headers ParsedHeaders
	lines := strings.Split(text, "\n")

	// Only scan lines before the first blank line (header section)
	for _, line := range lines {
		if line == "" {
			break
		}
		switch {
		case strings.HasPrefix(line, "From: "):
			headers.From = strings.TrimPrefix(line, "From: ")
		case strings.HasPrefix(line, "To: "):
			headers.To = strings.TrimPrefix(line, "To: ")
		case strings.HasPrefix(line, "CC: "):
			headers.CC = strings.TrimPrefix(line, "CC: ")
		case strings.HasPrefix(line, "Reply-To: "):
			headers.ReplyTo = strings.TrimPrefix(line, "Reply-To: ")
		case strings.HasPrefix(line, "Subject: "):
			headers.Subject = strings.TrimPrefix(line, "Subject: ")
		}
	}

	if headers.From == "" {
		return ParsedHeaders{}, errMissingFromHeader
	}
	return headers, nil
}

// ComposeReplyAddresses determines the from, to, cc, and subject for a reply email.
func ComposeReplyAddresses(headers ParsedHeaders) (from string, to []string, cc []string, subject string) {
	toAddresses := splitAddresses(headers.To)
	if len(toAddresses) > 0 {
		from = toAddresses[0]
	}

	if headers.ReplyTo != "" {
		to = []string{headers.ReplyTo}
	} else {
		to = []string{headers.From}
	}

	var allCC []string
	if len(toAddresses) > 1 {
		allCC = append(allCC, toAddresses[1:]...)
	}
	if headers.CC != "" {
		allCC = append(allCC, splitAddresses(headers.CC)...)
	}
	for _, addr := range allCC {
		trimmed := strings.TrimSpace(addr)
		if trimmed != from {
			cc = append(cc, trimmed)
		}
	}

	subject = headers.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	return from, to, cc, subject
}

func splitAddresses(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// SendReplyEmail sends a reply email via SMTP using the given configuration.
func SendReplyEmail(
	config *SMTPOutConfig,
	from string,
	to []string,
	cc []string,
	subject string,
	body string,
) error {
	m := gomail.NewMessage()
	m.SetHeader("From", from)
	m.SetHeader("To", to...)
	if len(cc) > 0 {
		m.SetHeader("Cc", cc...)
	}
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", body)

	d := gomail.NewDialer(config.Host, config.Port, config.Username, config.Password)
	return d.DialAndSend(m)
}

// HandleTelegramReply processes a Telegram update that is a reply to a bot message,
// extracts email headers from the original message, and sends a reply email.
func HandleTelegramReply(update TelegramUpdate, smtpOutConfig *SMTPOutConfig, botUserID int64) (notification string, err error) {
	msg := update.Message
	if msg == nil || msg.ReplyToMessage == nil {
		return "", nil
	}

	// Only handle replies to our own messages — silently ignore others
	if msg.ReplyToMessage.From == nil || msg.ReplyToMessage.From.ID != botUserID {
		return "", nil
	}

	if !smtpOutConfig.IsConfigured() {
		return "Reply-to-email is not configured. Set ST_SMTP_OUT_HOST to enable.", nil
	}

	headers, err := ParseMessageHeaders(msg.ReplyToMessage.Text)
	if err != nil {
		return "Could not parse the original email from the message.", nil
	}

	from, to, cc, subject := ComposeReplyAddresses(headers)
	err = SendReplyEmail(smtpOutConfig, from, to, cc, subject, msg.Text)
	if err != nil {
		return fmt.Sprintf("Failed to send email: %s", err), nil
	}

	allRecipients := slices.Concat(to, cc)
	return fmt.Sprintf("Email sent from %s to %s", from, strings.Join(allRecipients, ", ")), nil
}
