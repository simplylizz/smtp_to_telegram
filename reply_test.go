package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMessageHeaders(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		want    ParsedHeaders
		wantErr bool
	}{
		{
			name: "full headers",
			text: "From: sender@test\nTo: recipient@test\nCC: cc@test\nReply-To: reply@test\nSubject: Hello\n\nBody text",
			want: ParsedHeaders{From: "sender@test", To: "recipient@test", CC: "cc@test", ReplyTo: "reply@test", Subject: "Hello"},
		},
		{
			name: "no CC or Reply-To",
			text: "From: sender@test\nTo: recipient@test\nSubject: Hello\n\nBody text",
			want: ParsedHeaders{From: "sender@test", To: "recipient@test", Subject: "Hello"},
		},
		{
			name: "body contains From: line - not parsed",
			text: "From: sender@test\nTo: recipient@test\nSubject: Hello\n\nFrom: not-a-header@test",
			want: ParsedHeaders{From: "sender@test", To: "recipient@test", Subject: "Hello"},
		},
		{name: "missing From", text: "To: recipient@test\nSubject: Hello\n\nBody", wantErr: true},
		{name: "empty text", text: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMessageHeaders(tt.text)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseChatIDs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []int64
		wantErr bool
	}{
		{name: "single", input: "123", want: []int64{123}},
		{name: "multiple", input: "123,456,789", want: []int64{123, 456, 789}},
		{name: "negative IDs (group chats)", input: "-100123,-100456", want: []int64{-100123, -100456}},
		{name: "with spaces", input: " 123 , 456 ", want: []int64{123, 456}},
		{name: "empty string", input: "", want: nil},
		{name: "invalid returns error", input: "123,abc,456", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseChatIDs(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestComposeReplyAddresses(t *testing.T) {
	anyHost := []string{"."}

	tests := []struct {
		name         string
		headers      ParsedHeaders
		allowedHosts []string
		wantFrom     string
		wantTo       []string
		wantCC       []string
		wantSubject  string
		wantErr      bool
	}{
		{
			name:        "reply to sender, use Reply-To",
			headers:     ParsedHeaders{From: "sender@test", To: "me@test", ReplyTo: "replyto@test", Subject: "Hello"},
			wantFrom:    "me@test",
			wantTo:      []string{"replyto@test"},
			wantCC:      nil,
			wantSubject: "Re: Hello",
		},
		{
			name:        "reply to sender, no Reply-To",
			headers:     ParsedHeaders{From: "sender@test", To: "me@test", Subject: "Hello"},
			wantFrom:    "me@test",
			wantTo:      []string{"sender@test"},
			wantCC:      nil,
			wantSubject: "Re: Hello",
		},
		{
			name:        "reply all with CC",
			headers:     ParsedHeaders{From: "sender@test", To: "me@test", CC: "cc1@test, cc2@test", Subject: "Hello"},
			wantFrom:    "me@test",
			wantTo:      []string{"sender@test"},
			wantCC:      []string{"cc1@test", "cc2@test"},
			wantSubject: "Re: Hello",
		},
		{
			name:        "reply all, exclude self from CC",
			headers:     ParsedHeaders{From: "sender@test", To: "me@test, other@test", CC: "cc@test", Subject: "Hello"},
			wantFrom:    "me@test",
			wantTo:      []string{"sender@test"},
			wantCC:      []string{"other@test", "cc@test"},
			wantSubject: "Re: Hello",
		},
		{
			name:        "subject already has Re:",
			headers:     ParsedHeaders{From: "sender@test", To: "me@test", Subject: "Re: Hello"},
			wantFrom:    "me@test",
			wantTo:      []string{"sender@test"},
			wantCC:      nil,
			wantSubject: "Re: Hello",
		},
		{
			name:        "subject RE: uppercase",
			headers:     ParsedHeaders{From: "sender@test", To: "me@test", Subject: "RE: Hello"},
			wantFrom:    "me@test",
			wantTo:      []string{"sender@test"},
			wantCC:      nil,
			wantSubject: "RE: Hello",
		},
		{
			name:        "subject re: lowercase",
			headers:     ParsedHeaders{From: "sender@test", To: "me@test", Subject: "re: Hello"},
			wantFrom:    "me@test",
			wantTo:      []string{"sender@test"},
			wantCC:      nil,
			wantSubject: "re: Hello",
		},
		{
			name:         "our address is in CC, not To",
			headers:      ParsedHeaders{From: "sender@example.com", To: "someone@example.com", CC: "me@myhost.org", Subject: "Hello"},
			allowedHosts: []string{"myhost.org"},
			wantFrom:     "me@myhost.org",
			wantTo:       []string{"sender@example.com"},
			wantCC:       []string{"someone@example.com"},
			wantSubject:  "Re: Hello",
		},
		{
			name:         "our address is second in To",
			headers:      ParsedHeaders{From: "sender@example.com", To: "other@example.com, me@myhost.org", Subject: "Hello"},
			allowedHosts: []string{"myhost.org"},
			wantFrom:     "me@myhost.org",
			wantTo:       []string{"sender@example.com"},
			wantCC:       []string{"other@example.com"},
			wantSubject:  "Re: Hello",
		},
		{
			name:         "no matching address returns error",
			headers:      ParsedHeaders{From: "sender@example.com", To: "someone@example.com", Subject: "Hello"},
			allowedHosts: []string{"myhost.org"},
			wantErr:      true,
		},
		{
			name:    "empty To and CC returns error",
			headers: ParsedHeaders{From: "sender@test", Subject: "Hello"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hosts := tt.allowedHosts
			if hosts == nil {
				hosts = anyHost
			}
			from, to, cc, subject, err := ComposeReplyAddresses(&tt.headers, hosts)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantFrom, from)
			require.Equal(t, tt.wantTo, to)
			require.Equal(t, tt.wantCC, cc)
			require.Equal(t, tt.wantSubject, subject)
		})
	}
}

func TestFindOwnAddress(t *testing.T) {
	tests := []struct {
		name         string
		addresses    []string
		allowedHosts []string
		want         string
	}{
		{
			name:         "any host returns first",
			addresses:    []string{"a@foo.com", "b@bar.com"},
			allowedHosts: []string{"."},
			want:         "a@foo.com",
		},
		{
			name:         "matches specific host",
			addresses:    []string{"a@foo.com", "b@bar.com"},
			allowedHosts: []string{"bar.com"},
			want:         "b@bar.com",
		},
		{
			name:         "case insensitive domain match",
			addresses:    []string{"me@MyHost.Org"},
			allowedHosts: []string{"myhost.org"},
			want:         "me@MyHost.Org",
		},
		{
			name:         "no match returns empty",
			addresses:    []string{"a@foo.com"},
			allowedHosts: []string{"bar.com"},
			want:         "",
		},
		{
			name:         "empty addresses",
			addresses:    nil,
			allowedHosts: []string{"."},
			want:         "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findOwnAddress(tt.addresses, tt.allowedHosts)
			require.Equal(t, tt.want, got)
		})
	}
}

type testEmail struct {
	from string
	to   []string
	data string
}

// runTestSMTPServer accepts a single SMTP connection and captures the message.
func runTestSMTPServer(t *testing.T, ln net.Listener, received chan<- testEmail) {
	t.Helper()
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()

	write := func(s string) { _, _ = fmt.Fprintf(conn, "%s\r\n", s) }
	scanner := bufio.NewScanner(conn)

	var email testEmail
	write("220 test SMTP server ready")

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "EHLO") || strings.HasPrefix(line, "HELO"):
			write("250-test Hello")
			write("250 OK")
		case strings.HasPrefix(line, "MAIL FROM:"):
			email.from = strings.TrimSpace(strings.TrimPrefix(line, "MAIL FROM:"))
			email.from = strings.Trim(email.from, "<>")
			write("250 OK")
		case strings.HasPrefix(line, "RCPT TO:"):
			addr := strings.TrimSpace(strings.TrimPrefix(line, "RCPT TO:"))
			addr = strings.Trim(addr, "<>")
			email.to = append(email.to, addr)
			write("250 OK")
		case strings.HasPrefix(line, "DATA"):
			write("354 Start mail input")
			var data strings.Builder
			for scanner.Scan() {
				dataLine := scanner.Text()
				if dataLine == "." {
					break
				}
				data.WriteString(dataLine)
				data.WriteString("\n")
			}
			email.data = data.String()
			write("250 OK")
		case strings.HasPrefix(line, "QUIT"):
			write("221 Bye")
			received <- email
			return
		default:
			write("250 OK")
		}
	}
}

func makeBotReplyUpdate(replyFromID int64, originalText, replyText string) TelegramUpdate {
	return TelegramUpdate{
		UpdateID: 1,
		Message: &TelegramUpdateMessage{
			MessageID: 100,
			Chat:      TelegramChat{ID: 42},
			Text:      replyText,
			ReplyToMessage: &TelegramReplyMessage{
				MessageID: 50,
				From:      &TelegramUser{ID: replyFromID, IsBot: true},
				Text:      originalText,
			},
		},
	}
}

func TestSendReplyEmail(t *testing.T) {
	received := make(chan testEmail, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go runTestSMTPServer(t, ln, received)

	addr := ln.Addr().String()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	config := &SMTPOutConfig{Host: host, Port: port}

	err = SendReplyEmail(config, "me@test", []string{"sender@test"}, nil, "Re: Hello", "Thanks!")
	require.NoError(t, err)

	msg := <-received
	require.Equal(t, "me@test", msg.from)
	require.Contains(t, msg.to, "sender@test")
	require.Contains(t, msg.data, "Re: Hello")
	require.Contains(t, msg.data, "Thanks!")
}

func TestHandleTelegramReply_Success(t *testing.T) {
	received := make(chan testEmail, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go runTestSMTPServer(t, ln, received)

	addr := ln.Addr().String()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	config := &SMTPOutConfig{Host: host, Port: port}
	originalText := "From: sender@test\nTo: me@test\nSubject: Hello\n\nOriginal body"
	update := makeBotReplyUpdate(999, originalText, "My reply")

	notification := HandleTelegramReply(update, config, 999, []string{"."})
	require.Contains(t, notification, "Email sent from me@test to sender@test")

	msg := <-received
	require.Equal(t, "me@test", msg.from)
	require.Contains(t, msg.to, "sender@test")
	require.Contains(t, msg.data, "My reply")
}

func TestHandleTelegramReply_NonBotMessage_Ignored(t *testing.T) {
	update := makeBotReplyUpdate(888, "From: sender@test\nTo: me@test\nSubject: Hello\n\nBody", "Reply text")
	config := &SMTPOutConfig{Host: "localhost", Port: 25}

	notification := HandleTelegramReply(update, config, 999, []string{"."})
	require.Empty(t, notification)
}

func TestHandleTelegramReply_SMTPNotConfigured(t *testing.T) {
	update := makeBotReplyUpdate(999, "From: sender@test\nTo: me@test\nSubject: Hello\n\nBody", "Reply text")
	config := &SMTPOutConfig{Host: "", Port: 0}

	notification := HandleTelegramReply(update, config, 999, []string{"."})
	require.Contains(t, notification, "not configured")
}

func TestHandleTelegramReply_ParseFailure(t *testing.T) {
	update := makeBotReplyUpdate(999, "just some random text", "Reply text")
	config := &SMTPOutConfig{Host: "localhost", Port: 25}

	notification := HandleTelegramReply(update, config, 999, []string{"."})
	require.Contains(t, notification, "Could not parse")
}

func TestHandleTelegramReply_SMTPSendFailure(t *testing.T) {
	update := makeBotReplyUpdate(999, "From: sender@test\nTo: me@test\nSubject: Hello\n\nBody", "Reply text")
	config := &SMTPOutConfig{Host: "127.0.0.1", Port: 19999}

	notification := HandleTelegramReply(update, config, 999, []string{"."})
	require.Contains(t, notification, "Failed to send email")
}

func TestEndToEndReplyFlow(t *testing.T) {
	// Setup: SMTP server + mock Telegram + test outbound SMTP
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	// Step 1: Send email via SMTP -> forwarded to Telegram
	err := smtp.SendMail(smtpConfig.Listen, nil, "sender@test", []string{"recipient@test"}, []byte(
		"Subject: Test subject\r\n\r\nTest body",
	))
	require.NoError(t, err)
	require.NotEmpty(t, h.RequestMessages)

	// Step 2: Start test SMTP server for outbound email
	received := make(chan testEmail, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	go runTestSMTPServer(t, ln, received)

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	smtpOutConfig := &SMTPOutConfig{Host: host, Port: port}

	// Step 3: Simulate Telegram reply to the forwarded message
	originalMessage := h.RequestMessages[0]
	update := makeBotReplyUpdate(999, originalMessage, "This is my reply!")

	// Step 4: Handle the reply
	notification := HandleTelegramReply(update, smtpOutConfig, 999, []string{"."})
	require.Contains(t, notification, "Email sent")

	// Step 5: Verify outbound email
	msg := <-received
	require.Contains(t, msg.data, "Test subject")
	require.Contains(t, msg.data, "This is my reply!")
}
