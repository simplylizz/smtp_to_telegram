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
