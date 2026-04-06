# Reply-to-Email Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable users to reply to forwarded Telegram messages to send emails back to original senders/participants.

**Architecture:** New file `reply.go` handles Telegram polling, message parsing, and outbound SMTP. The existing forward path is modified to include CC/Reply-To headers and ForceReply markup. Custom message templates are removed; the format is fixed.

**Tech Stack:** Go, gomail.v2 (outbound SMTP), Telegram Bot API (getUpdates long polling)

**Spec:** `docs/superpowers/specs/2026-03-22-reply-to-email-design.md`

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `smtp_to_telegram.go` | Modify | Remove template config, add CC/ReplyTo to FormattedEmail, update FormatMessage to fixed format with empty-line omission, add ForceReply to SendMessageToChat, wire polling into main/shutdown |
| `reply.go` | Create | SMTPOutConfig, Telegram polling loop (with backoff + stale update flushing), message header parsing, email composition, notification sending |
| `reply_test.go` | Create | Tests for parsing, email composition, polling, notification paths, test SMTP server helper |
| `smtp_to_telegram_test.go` | Modify | Update tests for removed template config, new fixed format, ForceReply, SuccessHandler changes |
| `README.md` | Modify | Document reply feature, new config, remove custom template docs |
| `env_file.example` | Modify | Add SMTP outbound env vars |

---

### Task 1: Remove custom message template support and add CC/Reply-To

This task combines template removal with CC/Reply-To support since they both modify `FormatMessage` and `FormattedEmail`.

**Files:**
- Modify: `smtp_to_telegram.go:82-92` (TelegramConfig struct)
- Modify: `smtp_to_telegram.go:104-111` (FormattedEmail struct)
- Modify: `smtp_to_telegram.go:133-290` (main, CLI flags)
- Modify: `smtp_to_telegram.go:682-862` (FormatEmail, FormatMessage)
- Modify: `smtp_to_telegram_test.go`

- [ ] **Step 1: Remove MessageTemplate from TelegramConfig struct**

In `smtp_to_telegram.go`, remove the `MessageTemplate` field from `TelegramConfig` struct (line 87).

- [ ] **Step 2: Add CC and ReplyTo to FormattedEmail struct**

```go
type FormattedEmail struct {
    From        string
    To          string
    CC          string
    ReplyTo     string
    Subject     string
    Text        string
    HTML        string
    Attachments []*FormattedAttachment
}
```

- [ ] **Step 3: Remove message-template CLI flag**

In `smtp_to_telegram.go`, remove the `message-template` CLI flag (lines 239-244) and the `MessageTemplate` assignment in the action (line 174).

- [ ] **Step 4: Add startup error for deprecated env var**

In `smtp_to_telegram.go` main action, right after the blacklist-file check:

```go
if os.Getenv("ST_TELEGRAM_MESSAGE_TEMPLATE") != "" {
    return errors.New("ST_TELEGRAM_MESSAGE_TEMPLATE is no longer supported. " +
        "The message format is now fixed to support the reply-to-email feature. " +
        "This check can be removed in 3.0.0 or later")
}
```

- [ ] **Step 5: Rewrite FormatMessage to use fixed format**

The function no longer takes a template. New signature:

```go
func FormatMessage(
    from, to, subject, text string,
    cc, replyTo string,
    formattedAttachmentsDetails string,
    messageLengthToSendAsFile uint,
) (fullMessageText, truncatedMessageText string) {
    var header strings.Builder
    fmt.Fprintf(&header, "From: %s\nTo: %s\n", from, to)
    if strings.TrimSpace(cc) != "" {
        fmt.Fprintf(&header, "CC: %s\n", cc)
    }
    if strings.TrimSpace(replyTo) != "" {
        fmt.Fprintf(&header, "Reply-To: %s\n", replyTo)
    }
    fmt.Fprintf(&header, "Subject: %s", subject)

    body := strings.TrimSpace(text)
    fullMessageText = header.String() + "\n\n" + body
    if formattedAttachmentsDetails != "" {
        fullMessageText += "\n\n" + formattedAttachmentsDetails
    }
    fullMessageText = strings.TrimSpace(fullMessageText)

    fullMessageRunes := []rune(fullMessageText)
    if uint(len(fullMessageRunes)) <= messageLengthToSendAsFile {
        return fullMessageText, ""
    }

    emptyBody := fmt.Sprintf(".%s", BodyTruncated)
    emptyMessageText := header.String() + "\n\n" + strings.TrimSpace(emptyBody)
    if formattedAttachmentsDetails != "" {
        emptyMessageText += "\n\n" + formattedAttachmentsDetails
    }
    emptyMessageText = strings.TrimSpace(emptyMessageText)
    emptyMessageRunes := []rune(emptyMessageText)
    if uint(len(emptyMessageRunes)) >= messageLengthToSendAsFile {
        return fullMessageText, string(fullMessageRunes[:messageLengthToSendAsFile])
    }

    maxBodyLength := messageLengthToSendAsFile - uint(len(emptyMessageRunes))
    truncatedBody := string([]rune(strings.TrimSpace(text))[:maxBodyLength])
    truncatedMessageText = header.String() + "\n\n" + strings.TrimSpace(truncatedBody+BodyTruncated)
    if formattedAttachmentsDetails != "" {
        truncatedMessageText += "\n\n" + formattedAttachmentsDetails
    }
    truncatedMessageText = strings.TrimSpace(truncatedMessageText)

    if uint(len([]rune(truncatedMessageText))) > messageLengthToSendAsFile {
        panic(fmt.Errorf("%w: maxBodyLength=%d, text=%s", errUnexpectedTruncation, maxBodyLength, truncatedMessageText))
    }
    return fullMessageText, truncatedMessageText
}
```

- [ ] **Step 6: Update FormatEmail to extract CC/Reply-To and call updated FormatMessage**

```go
replyTo := env.GetHeader("Reply-To")
cc := env.GetHeader("Cc")

fullMessageText, truncatedMessageText := FormatMessage(
    from, to, subject, text,
    cc, replyTo,
    formattedAttachmentsDetails,
    telegramConfig.MessageLengthToSendAsFile,
)
```

Populate the new fields in the returned `FormattedEmail` structs (`CC: cc, ReplyTo: replyTo`).

- [ ] **Step 7: Update tests**

- Remove `MessageTemplate` from `makeTelegramConfig()`
- Delete `TestSuccessCustomFormat` (tests custom templates which no longer exist)
- Add `TestCCAndReplyToInForwardedMessage`:

```go
func TestCCAndReplyToInForwardedMessage(t *testing.T) {
    smtpConfig := makeSMTPConfig()
    telegramConfig := makeTelegramConfig()
    d := startSMTP(t, smtpConfig, telegramConfig)
    defer d.Shutdown()

    h := NewSuccessHandler()
    s := HTTPServer(t, h)
    defer func() { _ = s.Shutdown(context.Background()) }()

    m := gomail.NewMessage()
    m.SetHeader("From", "sender@test")
    m.SetHeader("To", "to@test")
    m.SetHeader("Cc", "cc1@test, cc2@test")
    m.SetHeader("Reply-To", "replyto@test")
    m.SetHeader("Subject", "Test subj")
    m.SetBody("text/plain", "Hello")

    di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
    err := di.DialAndSend(m)
    require.NoError(t, err)

    exp :=
        "From: sender@test\n" +
            "To: to@test\n" +
            "CC: cc1@test, cc2@test\n" +
            "Reply-To: replyto@test\n" +
            "Subject: Test subj\n" +
            "\n" +
            "Hello"
    require.Equal(t, exp, h.RequestMessages[0])
}
```

- Add `TestNoCCOrReplyToWhenEmpty`:

```go
func TestNoCCOrReplyToWhenEmpty(t *testing.T) {
    smtpConfig := makeSMTPConfig()
    telegramConfig := makeTelegramConfig()
    d := startSMTP(t, smtpConfig, telegramConfig)
    defer d.Shutdown()

    h := NewSuccessHandler()
    s := HTTPServer(t, h)
    defer func() { _ = s.Shutdown(context.Background()) }()

    err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
    require.NoError(t, err)

    msg := h.RequestMessages[0]
    require.NotContains(t, msg, "CC:")
    require.NotContains(t, msg, "Reply-To:")
}
```

- [ ] **Step 8: Run tests**

Run: `go test ./... -v -count=1`
Expected: All tests pass with the new fixed format.

- [ ] **Step 9: Commit**

```bash
git add smtp_to_telegram.go smtp_to_telegram_test.go
git commit -m "feat!: remove custom templates, use fixed format with CC/Reply-To

BREAKING CHANGE: ST_TELEGRAM_MESSAGE_TEMPLATE is no longer supported.
The message format is now fixed to support the reply-to-email feature."
```

---

### Task 2: Add SMTP outbound configuration

Must come before ForceReply (Task 3) since ForceReply depends on knowing whether SMTP out is configured.

**Files:**
- Create: `reply.go`
- Modify: `smtp_to_telegram.go:66-68` (FilterConfig → AppConfig)
- Modify: `smtp_to_telegram.go:133-290` (main, CLI flags)
- Modify: `smtp_to_telegram.go:302-353` (loadFilterRules → loadConfig)
- Modify: `env_file.example`

- [ ] **Step 1: Create reply.go with SMTPOutConfig struct**

```go
package main

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
```

- [ ] **Step 2: Add CLI flags for SMTP outbound**

In `smtp_to_telegram.go`, add four new CLI flags:

```go
&cli.StringFlag{
    Name:    "smtp-out-host",
    Usage:   "Outbound SMTP server host for reply-to-email feature",
    Sources: cli.EnvVars("ST_SMTP_OUT_HOST"),
},
&cli.IntFlag{
    Name:    "smtp-out-port",
    Usage:   "Outbound SMTP server port",
    Value:   587,
    Sources: cli.EnvVars("ST_SMTP_OUT_PORT"),
},
&cli.StringFlag{
    Name:    "smtp-out-username",
    Usage:   "Outbound SMTP server username",
    Sources: cli.EnvVars("ST_SMTP_OUT_USERNAME"),
},
&cli.StringFlag{
    Name:    "smtp-out-password",
    Usage:   "Outbound SMTP server password",
    Sources: cli.EnvVars("ST_SMTP_OUT_PASSWORD"),
},
```

- [ ] **Step 3: Add YAML config support for smtp_out**

Rename `FilterConfig` to `AppConfig`:

```go
type AppConfig struct {
    FilterRules []FilterRule `yaml:"filter_rules"`
    SMTPOut     struct {
        Host     string `yaml:"host"`
        Port     int    `yaml:"port"`
        Username string `yaml:"username"`
        Password string `yaml:"password"`
    } `yaml:"smtp_out"`
}
```

Rename `loadFilterRules` to `loadConfig`. It now returns `*SMTPOutConfig` (from YAML) in addition to loading filter rules:

```go
func loadConfig(filename string) (*SMTPOutConfig, error) {
    filterRules = nil
    if filename == "" {
        return nil, nil
    }

    data, err := os.ReadFile(filename)
    if err != nil {
        return nil, fmt.Errorf("failed to read config file: %w", err)
    }

    var config AppConfig
    if err := yaml.Unmarshal(data, &config); err != nil {
        return nil, fmt.Errorf("failed to parse config YAML: %w", err)
    }

    // ... existing filter rules compilation logic unchanged ...

    filterRules = config.FilterRules
    if logger != nil {
        logger.Infof("Loaded %d filter rules from %s", len(filterRules), filename)
    }

    var yamlSMTPOut *SMTPOutConfig
    if config.SMTPOut.Host != "" {
        yamlSMTPOut = &SMTPOutConfig{
            Host:     config.SMTPOut.Host,
            Port:     config.SMTPOut.Port,
            Username: config.SMTPOut.Username,
            Password: config.SMTPOut.Password,
        }
    }
    return yamlSMTPOut, nil
}
```

- [ ] **Step 4: Wire SMTPOutConfig into main action**

Construct `SMTPOutConfig` from CLI flags, with YAML fallback. CLI/env values take precedence:

```go
// In main action, after loadConfig:
yamlSMTPOut, err := loadConfig(smtpConfig.ConfigFile)
// ... (replace existing loadFilterRules call)

smtpOutConfig := &SMTPOutConfig{
    Host:     cmd.String("smtp-out-host"),
    Port:     int(cmd.Int("smtp-out-port")),
    Username: cmd.String("smtp-out-username"),
    Password: cmd.String("smtp-out-password"),
}
// Apply YAML fallbacks when CLI values are empty/default
if yamlSMTPOut != nil {
    if smtpOutConfig.Host == "" {
        smtpOutConfig.Host = yamlSMTPOut.Host
    }
    if smtpOutConfig.Port == 587 && yamlSMTPOut.Port != 0 {
        smtpOutConfig.Port = yamlSMTPOut.Port
    }
    if smtpOutConfig.Username == "" {
        smtpOutConfig.Username = yamlSMTPOut.Username
    }
    if smtpOutConfig.Password == "" {
        smtpOutConfig.Password = yamlSMTPOut.Password
    }
}
```

- [ ] **Step 5: Update env_file.example**

Add commented-out SMTP outbound variables:

```
# ST_SMTP_OUT_HOST=smtp.example.com
# ST_SMTP_OUT_PORT=587
# ST_SMTP_OUT_USERNAME=user@example.com
# ST_SMTP_OUT_PASSWORD=secret
```

- [ ] **Step 6: Run tests**

Run: `go test ./... -v -count=1`
Expected: All pass (no behavioral change yet)

- [ ] **Step 7: Commit**

```bash
git add reply.go smtp_to_telegram.go env_file.example
git commit -m "feat: add outbound SMTP configuration for reply feature"
```

---

### Task 3: Add ForceReply markup to forwarded messages

**Files:**
- Modify: `smtp_to_telegram.go:484-522` (SendEmailToTelegram)
- Modify: `smtp_to_telegram.go:524-582` (SendMessageToChat)
- Modify: `smtp_to_telegram_test.go`

- [ ] **Step 1: Update SuccessHandler to capture reply_markup**

Add `RequestReplyMarkups []string` to `SuccessHandler` and capture the field in `ServeHTTP`:

```go
type SuccessHandler struct {
    RequestMessages    []string
    RequestDocuments   []*FormattedAttachment
    RequestReplyMarkups []string
}

func NewSuccessHandler() *SuccessHandler {
    return &SuccessHandler{
        RequestMessages:    []string{},
        RequestDocuments:   []*FormattedAttachment{},
        RequestReplyMarkups: []string{},
    }
}

// In ServeHTTP, inside the sendMessage branch, after ParseForm:
s.RequestReplyMarkups = append(s.RequestReplyMarkups, r.PostForm.Get("reply_markup"))
```

- [ ] **Step 2: Write test for ForceReply when flag is true**

```go
func TestForceReplyIncludedWhenEnabled(t *testing.T) {
    smtpConfig := makeSMTPConfig()
    telegramConfig := makeTelegramConfig()
    telegramConfig.ForceReply = true
    d := startSMTP(t, smtpConfig, telegramConfig)
    defer d.Shutdown()

    h := NewSuccessHandler()
    s := HTTPServer(t, h)
    defer func() { _ = s.Shutdown(context.Background()) }()

    err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
    require.NoError(t, err)

    require.NotEmpty(t, h.RequestReplyMarkups)
    require.Contains(t, h.RequestReplyMarkups[0], "force_reply")
}

func TestForceReplyNotIncludedWhenDisabled(t *testing.T) {
    smtpConfig := makeSMTPConfig()
    telegramConfig := makeTelegramConfig()
    // ForceReply defaults to false
    d := startSMTP(t, smtpConfig, telegramConfig)
    defer d.Shutdown()

    h := NewSuccessHandler()
    s := HTTPServer(t, h)
    defer func() { _ = s.Shutdown(context.Background()) }()

    err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
    require.NoError(t, err)

    require.NotEmpty(t, h.RequestReplyMarkups)
    require.Equal(t, "", h.RequestReplyMarkups[0])
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./... -run "TestForceReply" -v -count=1`
Expected: FAIL

- [ ] **Step 4: Add ForceReply to TelegramConfig and SendMessageToChat**

Add `ForceReply bool` field to `TelegramConfig`. In the main action, set it based on `smtpOutConfig.IsConfigured()`.

In `SendMessageToChat`, add ForceReply markup to form data:

```go
if telegramConfig.ForceReply {
    formData.Set("reply_markup", `{"force_reply":true,"selective":true}`)
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./... -v -count=1`
Expected: All pass

- [ ] **Step 6: Commit**

```bash
git add smtp_to_telegram.go smtp_to_telegram_test.go
git commit -m "feat: add ForceReply markup when outbound SMTP is configured"
```

---

### Task 4: Implement Telegram message header parsing

**Files:**
- Modify: `reply.go`
- Create: `reply_test.go`

- [ ] **Step 1: Write tests for ParseMessageHeaders**

```go
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
            want: ParsedHeaders{
                From:    "sender@test",
                To:      "recipient@test",
                CC:      "cc@test",
                ReplyTo: "reply@test",
                Subject: "Hello",
            },
        },
        {
            name: "no CC or Reply-To",
            text: "From: sender@test\nTo: recipient@test\nSubject: Hello\n\nBody text",
            want: ParsedHeaders{
                From:    "sender@test",
                To:      "recipient@test",
                Subject: "Hello",
            },
        },
        {
            name: "body contains From: line - not parsed",
            text: "From: sender@test\nTo: recipient@test\nSubject: Hello\n\nFrom: not-a-header@test",
            want: ParsedHeaders{
                From:    "sender@test",
                To:      "recipient@test",
                Subject: "Hello",
            },
        },
        {
            name: "missing From",
            text: "To: recipient@test\nSubject: Hello\n\nBody",
            wantErr: true,
        },
        {
            name: "empty text",
            text: "",
            wantErr: true,
        },
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestParseMessageHeaders -v -count=1`
Expected: FAIL (function doesn't exist)

- [ ] **Step 3: Implement ParseMessageHeaders in reply.go**

```go
type ParsedHeaders struct {
    From    string
    To      string
    CC      string
    ReplyTo string
    Subject string
}

var errMissingFromHeader = errors.New("missing From header in message")

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
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestParseMessageHeaders -v -count=1`
Expected: All pass

- [ ] **Step 5: Commit**

```bash
git add reply.go reply_test.go
git commit -m "feat: add Telegram message header parsing for reply feature"
```

---

### Task 5: Implement reply email composition

**Files:**
- Modify: `reply.go`
- Modify: `reply_test.go`

- [ ] **Step 1: Write tests for ComposeReplyAddresses**

```go
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
            name: "reply to sender, use Reply-To",
            headers: ParsedHeaders{
                From: "sender@test", To: "me@test",
                ReplyTo: "replyto@test", Subject: "Hello",
            },
            wantFrom: "me@test", wantTo: []string{"replyto@test"},
            wantCC: nil, wantSubject: "Re: Hello",
        },
        {
            name: "reply to sender, no Reply-To",
            headers: ParsedHeaders{
                From: "sender@test", To: "me@test", Subject: "Hello",
            },
            wantFrom: "me@test", wantTo: []string{"sender@test"},
            wantCC: nil, wantSubject: "Re: Hello",
        },
        {
            name: "reply all with CC",
            headers: ParsedHeaders{
                From: "sender@test", To: "me@test",
                CC: "cc1@test, cc2@test", Subject: "Hello",
            },
            wantFrom: "me@test", wantTo: []string{"sender@test"},
            wantCC: []string{"cc1@test", "cc2@test"}, wantSubject: "Re: Hello",
        },
        {
            name: "reply all, exclude self from CC",
            headers: ParsedHeaders{
                From: "sender@test", To: "me@test, other@test",
                CC: "cc@test", Subject: "Hello",
            },
            wantFrom: "me@test", wantTo: []string{"sender@test"},
            wantCC: []string{"other@test", "cc@test"}, wantSubject: "Re: Hello",
        },
        {
            name: "subject already has Re: prefix",
            headers: ParsedHeaders{
                From: "sender@test", To: "me@test", Subject: "Re: Hello",
            },
            wantFrom: "me@test", wantTo: []string{"sender@test"},
            wantCC: nil, wantSubject: "Re: Hello",
        },
        {
            name: "subject with RE: uppercase",
            headers: ParsedHeaders{
                From: "sender@test", To: "me@test", Subject: "RE: Hello",
            },
            wantFrom: "me@test", wantTo: []string{"sender@test"},
            wantCC: nil, wantSubject: "RE: Hello",
        },
        {
            name: "subject re: lowercase",
            headers: ParsedHeaders{
                From: "sender@test", To: "me@test", Subject: "re: Hello",
            },
            wantFrom: "me@test", wantTo: []string{"sender@test"},
            wantCC: nil, wantSubject: "re: Hello",
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestComposeReplyAddresses -v -count=1`
Expected: FAIL

- [ ] **Step 3: Implement ComposeReplyAddresses in reply.go**

```go
func ComposeReplyAddresses(headers ParsedHeaders) (from string, to []string, cc []string, subject string) {
    // From: first address in original To
    toAddresses := splitAddresses(headers.To)
    if len(toAddresses) > 0 {
        from = toAddresses[0]
    }

    // To: Reply-To if present, otherwise original From
    if headers.ReplyTo != "" {
        to = []string{headers.ReplyTo}
    } else {
        to = []string{headers.From}
    }

    // CC: remaining To addresses + original CC, minus our own address
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

    // Subject: add Re: prefix if not already present (case-insensitive check)
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
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestComposeReplyAddresses -v -count=1`
Expected: All pass

- [ ] **Step 5: Commit**

```bash
git add reply.go reply_test.go
git commit -m "feat: add reply email address composition logic"
```

---

### Task 6: Implement outbound email sending

**Files:**
- Modify: `reply.go`
- Modify: `reply_test.go`

- [ ] **Step 1: Add test SMTP server helper to reply_test.go**

This is needed for testing outbound email sending. A minimal SMTP server that accepts one message:

```go
type testEmail struct {
    from string
    to   []string
    data string
}

// runTestSMTPServer accepts a single SMTP connection and captures the message.
// It implements the bare minimum SMTP protocol (EHLO, MAIL FROM, RCPT TO, DATA, QUIT).
func runTestSMTPServer(t *testing.T, ln net.Listener, received chan<- testEmail) {
    t.Helper()
    conn, err := ln.Accept()
    if err != nil {
        return
    }
    defer conn.Close()

    write := func(s string) { fmt.Fprintf(conn, "%s\r\n", s) }
    read := func() string {
        buf := make([]byte, 4096)
        n, _ := conn.Read(buf)
        return string(buf[:n])
    }

    var email testEmail
    write("220 test SMTP server ready")

    for {
        line := read()
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
            for {
                chunk := read()
                if strings.HasSuffix(strings.TrimSpace(chunk), "\r\n.\r\n") || strings.HasSuffix(strings.TrimSpace(chunk), "\n.\n") || chunk == ".\r\n" {
                    data.WriteString(strings.TrimSuffix(strings.TrimSuffix(chunk, ".\r\n"), "\r\n.\r\n"))
                    break
                }
                data.WriteString(chunk)
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
```

- [ ] **Step 2: Write test for SendReplyEmail**

```go
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./... -run TestSendReplyEmail -v -count=1`
Expected: FAIL

- [ ] **Step 4: Implement SendReplyEmail in reply.go**

```go
import "gopkg.in/gomail.v2"

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
```

- [ ] **Step 5: Run tests**

Run: `go test ./... -run TestSendReplyEmail -v -count=1`
Expected: Pass

- [ ] **Step 6: Commit**

```bash
git add reply.go reply_test.go
git commit -m "feat: add outbound email sending via gomail"
```

---

### Task 7: Implement HandleTelegramReply and all notification paths

**Files:**
- Modify: `reply.go`
- Modify: `reply_test.go`

- [ ] **Step 1: Define Telegram update types in reply.go**

```go
type TelegramUpdate struct {
    UpdateID int                    `json:"update_id"`
    Message  *TelegramUpdateMessage `json:"message"`
}

type TelegramUpdateMessage struct {
    MessageID      int                    `json:"message_id"`
    Chat           TelegramChat           `json:"chat"`
    Text           string                 `json:"text"`
    From           *TelegramUser          `json:"from"`
    ReplyToMessage *TelegramReplyMessage  `json:"reply_to_message"`
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
```

- [ ] **Step 2: Write tests for HandleTelegramReply — all paths**

```go
func TestHandleTelegramReply_Success(t *testing.T) {
    received := make(chan testEmail, 1)
    ln, err := net.Listen("tcp", "127.0.0.1:0")
    require.NoError(t, err)
    defer ln.Close()
    go runTestSMTPServer(t, ln, received)

    host, portStr, _ := net.SplitHostPort(ln.Addr().String())
    port, _ := strconv.Atoi(portStr)
    smtpOutConfig := &SMTPOutConfig{Host: host, Port: port}

    update := makeBotReplyUpdate(999, "From: sender@test\nTo: me@test\nSubject: Hello\n\nBody", "My reply")

    notification, err := HandleTelegramReply(update, smtpOutConfig, 999)
    require.NoError(t, err)
    require.Contains(t, notification, "Email sent from me@test to sender@test")

    msg := <-received
    require.Equal(t, "me@test", msg.from)
    require.Contains(t, msg.to, "sender@test")
}

func TestHandleTelegramReply_NonBotMessage_Ignored(t *testing.T) {
    smtpOutConfig := &SMTPOutConfig{Host: "smtp.test", Port: 587}
    // Reply to a message from user ID 888 (not the bot ID 999)
    update := makeBotReplyUpdate(888, "some text", "reply")

    notification, err := HandleTelegramReply(update, smtpOutConfig, 999)
    require.NoError(t, err)
    require.Equal(t, "", notification) // silently ignored
}

func TestHandleTelegramReply_SMTPNotConfigured(t *testing.T) {
    smtpOutConfig := &SMTPOutConfig{} // not configured
    update := makeBotReplyUpdate(999, "From: sender@test\nTo: me@test\nSubject: Hello\n\nBody", "reply")

    notification, err := HandleTelegramReply(update, smtpOutConfig, 999)
    require.NoError(t, err)
    require.Contains(t, notification, "not configured")
}

func TestHandleTelegramReply_ParseFailure(t *testing.T) {
    smtpOutConfig := &SMTPOutConfig{Host: "smtp.test", Port: 587}
    // Original message has no parseable headers
    update := makeBotReplyUpdate(999, "just some random text with no headers", "reply")

    notification, err := HandleTelegramReply(update, smtpOutConfig, 999)
    require.NoError(t, err)
    require.Contains(t, notification, "Could not parse")
}

func TestHandleTelegramReply_SMTPSendFailure(t *testing.T) {
    // Point to a non-existent SMTP server
    smtpOutConfig := &SMTPOutConfig{Host: "127.0.0.1", Port: 19999}
    update := makeBotReplyUpdate(999, "From: sender@test\nTo: me@test\nSubject: Hello\n\nBody", "reply")

    notification, err := HandleTelegramReply(update, smtpOutConfig, 999)
    require.NoError(t, err)
    require.Contains(t, notification, "Failed to send email")
}

// Helper to construct a TelegramUpdate simulating a reply to a bot message
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
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./... -run "TestHandleTelegramReply" -v -count=1`
Expected: FAIL

- [ ] **Step 4: Implement HandleTelegramReply**

```go
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
```

Note: uses `slices.Concat` (already imported in main file) to avoid `append` mutation bug.

- [ ] **Step 5: Run tests**

Run: `go test ./... -run "TestHandleTelegramReply" -v -count=1`
Expected: All pass

- [ ] **Step 6: Commit**

```bash
git add reply.go reply_test.go
git commit -m "feat: add Telegram reply handler with all notification paths"
```

---

### Task 8: Implement Telegram polling goroutine and wire into main

**Files:**
- Modify: `reply.go`
- Modify: `smtp_to_telegram.go` (main, awaitShutdown)

- [ ] **Step 1: Implement GetBotUserID with retry**

```go
func GetBotUserID(ctx context.Context, telegramConfig *TelegramConfig, client *http.Client) (int64, error) {
    apiURL := fmt.Sprintf("%sbot%s/getMe", telegramConfig.APIPrefix, telegramConfig.BotToken)
    maxRetries := 5
    backoff := 2 * time.Second

    for attempt := range maxRetries {
        if ctx.Err() != nil {
            return 0, ctx.Err()
        }

        req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
        if err != nil {
            return 0, fmt.Errorf("failed to create getMe request: %w", err)
        }
        resp, err := client.Do(req)
        if err != nil {
            logger.Warningf("getMe attempt %d/%d failed: %s", attempt+1, maxRetries, SanitizeBotToken(err.Error(), telegramConfig.BotToken))
            time.Sleep(backoff)
            backoff *= 2
            continue
        }

        var result TelegramGetMeResult
        decodeErr := json.NewDecoder(resp.Body).Decode(&result)
        if closeErr := resp.Body.Close(); closeErr != nil {
            logger.Warningf("Failed to close response body: %v", closeErr)
        }
        if decodeErr != nil {
            return 0, fmt.Errorf("failed to parse getMe response: %w", decodeErr)
        }
        if !result.Ok || result.Result == nil {
            return 0, fmt.Errorf("getMe returned not ok")
        }
        return result.Result.ID, nil
    }
    return 0, fmt.Errorf("getMe failed after %d retries", maxRetries)
}
```

- [ ] **Step 2: Implement getUpdates with proper URL encoding**

```go
func getUpdates(ctx context.Context, telegramConfig *TelegramConfig, client *http.Client, offset int) ([]TelegramUpdate, error) {
    apiURL := fmt.Sprintf("%sbot%s/getUpdates", telegramConfig.APIPrefix, telegramConfig.BotToken)
    params := url.Values{
        "timeout":         {"30"},
        "offset":          {fmt.Sprintf("%d", offset)},
        "allowed_updates": {`["message"]`},
    }
    fullURL := apiURL + "?" + params.Encode()

    req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
    if err != nil {
        return nil, err
    }
    resp, err := client.Do(req)
    if err != nil {
        return nil, err
    }
    defer func() {
        if closeErr := resp.Body.Close(); closeErr != nil {
            logger.Warningf("Failed to close response body: %v", closeErr)
        }
    }()

    var result TelegramGetUpdatesResult
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }
    if !result.Ok {
        return nil, fmt.Errorf("getUpdates returned not ok")
    }
    return result.Result, nil
}
```

- [ ] **Step 3: Implement sendNotification (reuse client)**

```go
func sendNotification(ctx context.Context, telegramConfig *TelegramConfig, client *http.Client, chatID int64, replyToMessageID int, text string) {
    apiURL := fmt.Sprintf(
        "%sbot%s/sendMessage",
        telegramConfig.APIPrefix,
        telegramConfig.BotToken,
    )
    formData := url.Values{
        "chat_id":             {fmt.Sprintf("%d", chatID)},
        "text":                {text},
        "reply_to_message_id": {fmt.Sprintf("%d", replyToMessageID)},
    }
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(formData.Encode()))
    if err != nil {
        logger.Errorf("Failed to create notification request: %s", err)
        return
    }
    req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
    resp, err := client.Do(req)
    if err != nil {
        logger.Errorf("Failed to send notification: %s", SanitizeBotToken(err.Error(), telegramConfig.BotToken))
        return
    }
    defer func() {
        if closeErr := resp.Body.Close(); closeErr != nil {
            logger.Warningf("Failed to close response body: %v", closeErr)
        }
    }()
    if resp.StatusCode != 200 {
        body, _ := io.ReadAll(resp.Body)
        logger.Errorf("Notification failed: (%d) %s", resp.StatusCode, SanitizeBotToken(string(body), telegramConfig.BotToken))
    }
}
```

- [ ] **Step 4: Implement PollTelegramUpdates with backoff and stale update flushing**

```go
func PollTelegramUpdates(
    ctx context.Context,
    telegramConfig *TelegramConfig,
    smtpOutConfig *SMTPOutConfig,
) {
    // Dedicated HTTP client with longer timeout for long polling
    client := &http.Client{Timeout: 40 * time.Second}

    botUserID, err := GetBotUserID(ctx, telegramConfig, client)
    if err != nil {
        logger.Errorf("Failed to get bot identity, reply feature disabled: %s", SanitizeBotToken(err.Error(), telegramConfig.BotToken))
        return
    }
    logger.Infof("Bot user ID: %d, starting Telegram polling", botUserID)

    // Flush stale updates on cold start: get pending updates and discard them
    offset := 0
    staleUpdates, err := getUpdates(ctx, telegramConfig, client, -1)
    if err == nil && len(staleUpdates) > 0 {
        offset = staleUpdates[len(staleUpdates)-1].UpdateID + 1
        logger.Infof("Discarded %d stale updates on startup", len(staleUpdates))
    }

    // Main polling loop with exponential backoff on errors
    errorBackoff := 5 * time.Second
    maxBackoff := 5 * time.Minute

    for {
        select {
        case <-ctx.Done():
            logger.Info("Telegram polling stopped")
            return
        default:
        }

        updates, err := getUpdates(ctx, telegramConfig, client, offset)
        if err != nil {
            if ctx.Err() != nil {
                return
            }
            logger.Errorf("getUpdates error: %s", SanitizeBotToken(err.Error(), telegramConfig.BotToken))
            time.Sleep(errorBackoff)
            errorBackoff = min(errorBackoff*2, maxBackoff)
            continue
        }
        // Reset backoff on success
        errorBackoff = 5 * time.Second

        for _, update := range updates {
            offset = update.UpdateID + 1
            notification, handleErr := HandleTelegramReply(update, smtpOutConfig, botUserID)
            if handleErr != nil {
                logger.Errorf("Error handling reply: %s", handleErr)
                continue
            }
            if notification != "" && update.Message != nil {
                sendNotification(ctx, telegramConfig, client, update.Message.Chat.ID, update.Message.MessageID, notification)
            }
        }
    }
}
```

- [ ] **Step 5: Wire polling into main**

In `smtp_to_telegram.go`, in the main action, after constructing `smtpOutConfig`:

```go
d, err := SMTPStart(smtpConfig, telegramConfig)
if err != nil {
    return fmt.Errorf("start error: %w", err)
}

var cancelPolling context.CancelFunc
if smtpOutConfig.IsConfigured() {
    telegramConfig.ForceReply = true
    pollCtx, cancel := context.WithCancel(context.Background())
    cancelPolling = cancel
    go PollTelegramUpdates(pollCtx, telegramConfig, smtpOutConfig)
}

return awaitShutdown(ctx, &d, cancelPolling)
```

- [ ] **Step 6: Update awaitShutdown signature and cancel polling on shutdown**

```go
func awaitShutdown(ctx context.Context, d *guerrilla.Daemon, cancelPolling context.CancelFunc) error {
    ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
    defer stop()

    <-ctx.Done()
    logger.Info("Shutdown signal caught")

    // Stop polling first
    if cancelPolling != nil {
        cancelPolling()
    }

    // Graceful shutdown of SMTP with timeout
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
    defer cancel()

    done := make(chan struct{})
    go func() {
        d.Shutdown()
        close(done)
    }()

    select {
    case <-done:
        logger.Info("Shutdown completed, exiting.")
        return nil
    case <-shutdownCtx.Done():
        logger.Error("graceful shutdown timed out")
        return shutdownCtx.Err()
    }
}
```

- [ ] **Step 7: Run all tests**

Run: `go test ./... -v -count=1`
Expected: All pass

- [ ] **Step 8: Commit**

```bash
git add reply.go smtp_to_telegram.go
git commit -m "feat: add Telegram polling loop with backoff and wire into main"
```

---

### Task 9: Update README and config example

**Files:**
- Modify: `README.md`
- Modify: `env_file.example`

- [ ] **Step 1: Update README.md**

- Add a new "Reply to Email" section after "Getting started"
- Describe the feature: reply to a forwarded Telegram message to send an email back
- Document new config options (env vars, CLI flags, YAML `smtp_out` section)
- Update the default message format (now fixed, includes CC/Reply-To when present)
- Remove custom template documentation (`ST_TELEGRAM_MESSAGE_TEMPLATE` section)
- Document the breaking change
- Document limitations: multiple `To:` addresses uses the first one as sender
- Show an example YAML config with all options including `smtp_out`

- [ ] **Step 2: Update env_file.example**

Ensure SMTP outbound variables are included (should already be from Task 2, verify).

- [ ] **Step 3: Commit**

```bash
git add README.md env_file.example
git commit -m "docs: document reply-to-email feature and configuration"
```

---

### Task 10: Final integration test and cleanup

**Files:**
- Modify: `reply_test.go`

- [ ] **Step 1: Write end-to-end integration test**

Test the full flow: send email via SMTP → bot forwards to Telegram (mock) → simulate a reply update via `HandleTelegramReply` → verify outbound email sent via test SMTP server → verify correct notification string.

This uses the existing mock HTTP server for Telegram API and the `runTestSMTPServer` for outbound email.

- [ ] **Step 2: Run all tests**

Run: `go test ./... -v -count=1`
Expected: All pass

- [ ] **Step 3: Run linter**

Run: `golangci-lint run`
Expected: No issues

- [ ] **Step 4: Commit**

```bash
git add reply_test.go
git commit -m "test: add end-to-end integration test for reply feature"
```
