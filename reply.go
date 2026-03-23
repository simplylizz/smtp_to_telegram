package main

import (
	"errors"
	"strings"
)

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
