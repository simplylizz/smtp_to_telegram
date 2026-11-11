package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// spamboxMaxHTMLSize limits the HTML part saved with each entry.
const spamboxMaxHTMLSize = 64 * 1024

// Spambox keeps a copy of the emails rejected by filter rules in a JSON Lines
// file, so the rules can be reviewed and tuned.
type Spambox struct {
	mu      sync.Mutex
	path    string
	maxSize int64 // rotate when the file would grow past this; 0 means never
}

// SpamboxEntry is one line of the spambox file.
type SpamboxEntry struct {
	Time         string `json:"time"`
	Rule         string `json:"rule"`
	From         string `json:"from"`
	EnvelopeFrom string `json:"envelope_from,omitempty"` // only if it differs from From
	To           string `json:"to"`
	Subject      string `json:"subject"`
	// Text is the message as it would have been posted to Telegram: the
	// header lines, then the body. It is what the "body" filter field matches.
	Text          string `json:"text"`
	HTML          string `json:"html,omitempty"`
	HTMLTruncated bool   `json:"html_truncated,omitempty"`
}

// spambox is nil when the feature is disabled.
var spambox *Spambox

// NewSpambox checks that the file can be written, so a wrong path or missing
// permissions show up at startup instead of on the first rejected email.
func NewSpambox(path string, maxSize int64) (*Spambox, error) {
	f, err := openSpamboxFile(path)
	if err != nil {
		return nil, fmt.Errorf("spambox file is not writable: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("spambox file is not writable: %w", err)
	}
	return &Spambox{path: path, maxSize: maxSize}, nil
}

func openSpamboxFile(path string) (*os.File, error) {
	//nolint:gosec // User-specified spambox path is intentional
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

// Add appends the rejected email together with the name of the rule that
// rejected it.
func (s *Spambox) Add(ruleName string, message *FormattedEmail, now time.Time) error {
	entry := SpamboxEntry{
		Time:    now.UTC().Format(time.RFC3339),
		Rule:    ruleName,
		From:    message.From,
		To:      message.To,
		Subject: message.Subject,
		Text:    message.Text,
		HTML:    message.HTML,
	}
	if message.EnvelopeFrom != message.From {
		entry.EnvelopeFrom = message.EnvelopeFrom
	}
	if len(entry.HTML) > spamboxMaxHTMLSize {
		// Cutting may split a multi-byte character; drop the broken tail.
		entry.HTML = strings.ToValidUTF8(entry.HTML[:spamboxMaxHTMLSize], "")
		entry.HTMLTruncated = true
	}

	var line bytes.Buffer
	enc := json.NewEncoder(&line) // ends the entry with a newline
	enc.SetEscapeHTML(false)      // keep <, > and & readable and greppable
	if err := enc.Encode(&entry); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.rotateIfNeeded(int64(line.Len())); err != nil {
		return err
	}
	// The file is reopened for every entry, so it can also be rotated or
	// removed from outside (e.g. by logrotate) without telling the program.
	f, err := openSpamboxFile(s.path)
	if err != nil {
		return err
	}
	// A single write keeps the line in one piece.
	_, err = f.Write(line.Bytes())
	return errors.Join(err, f.Close())
}

// rotateIfNeeded moves a full file to "<path>.1", replacing the previous one,
// so at most two files of maxSize are kept.
func (s *Spambox) rotateIfNeeded(entrySize int64) error {
	if s.maxSize <= 0 {
		return nil
	}
	info, err := os.Stat(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Size() == 0 || info.Size()+entrySize <= s.maxSize {
		return nil
	}
	return os.Rename(s.path, s.path+".1")
}
