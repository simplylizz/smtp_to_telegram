package main

import (
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
