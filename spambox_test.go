package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/gomail.v2"
)

const spamboxTestRules = `filter_rules:
  - name: block-spam-subject
    conditions:
      - field: subject
        pattern: 'spam test'
`

func writeSpamboxTestRules(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(spamboxTestRules), 0o600))
	return path
}

func sendSpamboxTestEmail(subject string) error {
	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", subject)
	m.SetBody("text/plain", "This is a test body")
	return gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "").DialAndSend(m)
}

func readSpambox(t *testing.T, path string) []SpamboxEntry {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // test file
	require.NoError(t, err)
	var entries []SpamboxEntry
	for line := range strings.Lines(string(content)) {
		require.True(t, strings.HasSuffix(line, "\n"), "entry must end with a newline")
		var e SpamboxEntry
		require.NoError(t, json.Unmarshal([]byte(line), &e))
		entries = append(entries, e)
	}
	return entries
}

func useSpambox(t *testing.T, s *Spambox) {
	t.Helper()
	spambox = s
	t.Cleanup(func() { spambox = nil })
}

func TestSpamboxSavesRejectedEmail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spambox.jsonl")
	s, err := NewSpambox(path, 0)
	require.NoError(t, err)
	useSpambox(t, s)

	smtpConfig := makeSMTPConfig()
	smtpConfig.ConfigFile = writeSpamboxTestRules(t)
	d := startSMTP(t, smtpConfig, makeTelegramConfig())
	defer d.Shutdown()

	err = sendSpamboxTestEmail("SPAM TEST message")
	require.Error(t, err)
	require.Contains(t, err.Error(), "554")

	entries := readSpambox(t, path)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Regexp(t, `^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$`, e.Time)
	e.Time = ""
	require.Equal(t, SpamboxEntry{
		Rule:    "block-spam-subject",
		From:    "from@test",
		To:      "to@test",
		Subject: "SPAM TEST message",
		Text:    "From: from@test\nTo: to@test\nSubject: SPAM TEST message\n\nThis is a test body",
	}, e)
}

func TestSpamboxSkipsAcceptedEmail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spambox.jsonl")
	s, err := NewSpambox(path, 0)
	require.NoError(t, err)
	useSpambox(t, s)

	smtpConfig := makeSMTPConfig()
	smtpConfig.ConfigFile = writeSpamboxTestRules(t)
	d := startSMTP(t, smtpConfig, makeTelegramConfig())
	defer d.Shutdown()

	h := NewSuccessHandler()
	srv := HTTPServer(t, h)
	defer func() { _ = srv.Shutdown(t.Context()) }()

	require.NoError(t, sendSpamboxTestEmail("A normal message"))

	content, err := os.ReadFile(path) //nolint:gosec // test file
	require.NoError(t, err)
	require.Empty(t, content)
}

func TestSpamboxWriteFailureStillRejects(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spambox.jsonl")
	s, err := NewSpambox(path, 0)
	require.NoError(t, err)
	useSpambox(t, s)

	// Replace the file with a directory so that appending fails.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))

	smtpConfig := makeSMTPConfig()
	smtpConfig.ConfigFile = writeSpamboxTestRules(t)
	d := startSMTP(t, smtpConfig, makeTelegramConfig())
	defer d.Shutdown()

	err = sendSpamboxTestEmail("SPAM TEST message")
	require.Error(t, err)
	require.Contains(t, err.Error(), "554")
	require.Contains(t, err.Error(), "email rejected by filter rule")
}

func TestNewSpamboxUnwritablePath(t *testing.T) {
	_, err := NewSpambox(filepath.Join(t.TempDir(), "missing-dir", "spambox.jsonl"), 0)
	require.Error(t, err)
	require.Contains(t, err.Error(), "spambox file is not writable")
}

func TestSpamboxEntryFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spambox.jsonl")
	s, err := NewSpambox(path, 0)
	require.NoError(t, err)

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	msg := &FormattedEmail{
		From:         "author@test",
		EnvelopeFrom: "bounce@esp.test",
		To:           "to@test",
		Subject:      "Hi",
		Text:         "From: author@test\n\nline one\nline two",
		HTML:         `<a href="https://example.com/?a=1&b=2">x</a>`,
	}
	require.NoError(t, s.Add("some-rule", msg, now))

	content, err := os.ReadFile(path) //nolint:gosec // test file
	require.NoError(t, err)
	// One line per email, with HTML left readable. The exact bytes matter
	// here, so the JSON is compared as a string.
	//nolint:testifylint // see above
	require.Equal(t,
		`{"time":"2026-01-02T03:04:05Z","rule":"some-rule","from":"author@test",`+
			`"envelope_from":"bounce@esp.test","to":"to@test","subject":"Hi",`+
			`"text":"From: author@test\n\nline one\nline two",`+
			`"html":"<a href=\"https://example.com/?a=1&b=2\">x</a>"}`+"\n",
		string(content))
}

func TestSpamboxTruncatesHTML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spambox.jsonl")
	s, err := NewSpambox(path, 0)
	require.NoError(t, err)

	// "é" takes two bytes, so the limit falls in the middle of a character.
	html := "x" + strings.Repeat("é", spamboxMaxHTMLSize)
	msg := &FormattedEmail{From: "a@test", EnvelopeFrom: "a@test", Text: "text", HTML: html}
	require.NoError(t, s.Add("rule", msg, time.Now()))

	entries := readSpambox(t, path)
	require.Len(t, entries, 1)
	require.True(t, entries[0].HTMLTruncated)
	require.Equal(t, html[:spamboxMaxHTMLSize-1], entries[0].HTML)
}

func TestSpamboxRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spambox.jsonl")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entry := func(text string) *FormattedEmail {
		return &FormattedEmail{From: "a@test", EnvelopeFrom: "a@test", Text: text}
	}
	read := func(p string) string {
		content, err := os.ReadFile(p) //nolint:gosec // test file
		require.NoError(t, err)
		return string(content)
	}

	// Each entry is ~110 bytes, so two fit into 250 bytes and the third doesn't.
	s, err := NewSpambox(path, 250)
	require.NoError(t, err)
	require.NoError(t, s.Add("rule", entry("first email body"), now))
	require.NoError(t, s.Add("rule", entry("second email body"), now))
	require.NoFileExists(t, path+".1")

	require.NoError(t, s.Add("rule", entry("third email body"), now))
	rotated := read(path + ".1")
	require.Contains(t, rotated, "first email body")
	require.Contains(t, rotated, "second email body")
	require.NotContains(t, read(path), "second email body")
	require.Contains(t, read(path), "third email body")

	// An entry larger than the limit is still saved whole, in a file of its own.
	big := strings.Repeat("x", 500)
	require.NoError(t, s.Add("rule", entry(big), now))
	require.Contains(t, read(path+".1"), "third email body")
	require.Contains(t, read(path), big)
}
