package main

import (
	"bufio"
	"fmt"
	"net"
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

func TestComposeReplyAddresses(t *testing.T) {
	tests := []struct {
		name        string
		headers     ParsedHeaders
		wantFrom    string
		wantTo      []string
		wantCC      []string
		wantSubject string
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			from, to, cc, subject := ComposeReplyAddresses(tt.headers)
			require.Equal(t, tt.wantFrom, from)
			require.Equal(t, tt.wantTo, to)
			require.Equal(t, tt.wantCC, cc)
			require.Equal(t, tt.wantSubject, subject)
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
	defer conn.Close()

	write := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
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

func TestSendReplyEmail(t *testing.T) {
	received := make(chan testEmail, 1)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
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
