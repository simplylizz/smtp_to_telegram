package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/phires/go-guerrilla"
	"github.com/stretchr/testify/require"
	"gopkg.in/gomail.v2"
)

var (
	testSMTPListenHost     = "127.0.0.1"
	testSMTPListenPort     = 22725
	testHTTPServerListen   = "127.0.0.1:22780"
	errTestUnexpectedValue = errors.New("unexpected value")
)

func makeSMTPConfig() *SMTPConfig {
	return &SMTPConfig{
		Listen:      fmt.Sprintf("%s:%d", testSMTPListenHost, testSMTPListenPort),
		PrimaryHost: "testhost",
	}
}

func makeTelegramConfig() *TelegramConfig {
	return &TelegramConfig{
		ChatIDs:                   "42,142",
		BotToken:                  "42:ZZZ",
		APIPrefix:                 "http://" + testHTTPServerListen + "/",
		MessageTemplate:           "From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}",
		ForwardedAttachmentMaxSize:         0,
		ForwardedAttachmentMaxPhotoSize:    0,
		ForwardedAttachmentRespectErrors:   true,
		MessageLengthToSendAsFile: 4095,
	}
}

func startSMTP(t *testing.T, smtpConfig *SMTPConfig, telegramConfig *TelegramConfig) guerrilla.Daemon {
	t.Helper()
	d, err := SMTPStart(smtpConfig, telegramConfig)
	if err != nil {
		t.Fatalf("start error: %s", err)
	}
	waitSMTP(smtpConfig.Listen)
	return d
}

func waitSMTP(smtpHost string) {
	for range 100 {
		c, err := smtp.Dial(smtpHost)
		if err == nil {
			_ = c.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func goMailBody(content []byte) gomail.FileSetting {
	return gomail.SetCopyFunc(func(w io.Writer) error {
		_, err := w.Write(content)
		return err
	})
}

func TestSuccess(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: \n" +
			"\n" +
			"hi"

	require.Equal(t, exp, h.RequestMessages[0])
}

func TestSuccessCustomFormat(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.MessageTemplate =
		"Subject: {subject}\\n\\n{body}"
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp := "Subject: \n" +
		"\n" +
		"hi"

	require.Equal(t, exp, h.RequestMessages[0])
}

func TestTelegramUnreachable(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	require.Error(t, err)
}

func TestTelegramHttpError(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	s := HTTPServer(t, &ErrorHandler{})
	defer func() { _ = s.Shutdown(context.Background()) }()

	err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(`hi`))
	require.Error(t, err)
}

func TestEncodedContent(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	b := []byte(
		"Subject: =?UTF-8?B?8J+Yjg==?=\r\n" +
			"Content-Type: text/plain; charset=UTF-8\r\n" +
			"Content-Transfer-Encoding: quoted-printable\r\n" +
			"\r\n" +
			"=F0=9F=92=A9\r\n")
	err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, b)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: 😎\n" +
			"\n" +
			"💩"
	require.Equal(t, exp, h.RequestMessages[0])
}

func TestHtmlAttachmentIsIgnored(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", "Text body")
	m.AddAlternative("text/html", "<p>HTML body</p>")

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err := di.DialAndSend(m)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Text body"
	require.Equal(t, exp, h.RequestMessages[0])
}

func TestAttachmentsDetails(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", "Text body")
	m.AddAlternative("text/html", "<p>HTML body</p>")
	// attachment txt file
	m.Attach("hey.txt", goMailBody([]byte("hi")))
	// inline image
	m.Embed("inline.jpg", goMailBody([]byte("JPG")))
	// attachment image
	m.Attach("attachment.jpg", goMailBody([]byte("JPG")))

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err := di.DialAndSend(m)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	require.Empty(t, h.RequestDocuments)
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Text body\n" +
			"\n" +
			"Attachments:\n" +
			"- 🔗 inline.jpg (image/jpeg) 3B, discarded\n" +
			"- 📎 hey.txt (text/plain) 2B, discarded\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, discarded"
	require.Equal(t, exp, h.RequestMessages[0])
}

func TestAttachmentsSending(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.ForwardedAttachmentMaxSize = 1024
	telegramConfig.ForwardedAttachmentMaxPhotoSize = 1024
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", "Text body")
	m.AddAlternative("text/html", "<p>HTML body</p>")
	// attachment txt file
	m.Attach("hey.txt", goMailBody([]byte("hi")))
	// inline image
	m.Embed("inline.jpg", goMailBody([]byte("JPG")))
	// attachment image
	m.Attach("attachment.jpg", goMailBody([]byte("JPG")))

	expFiles := []*FormattedAttachment{
		{
			Filename: "inline.jpg",
			Caption:  "inline.jpg",
			Content:  []byte("JPG"),
			FileType: AttachmentTypePhoto,
		},
		{
			Filename: "hey.txt",
			Caption:  "hey.txt",
			Content:  []byte("hi"),
			FileType: AttachmentTypeDocument,
		},
		{
			Filename: "attachment.jpg",
			Caption:  "attachment.jpg",
			Content:  []byte("JPG"),
			FileType: AttachmentTypePhoto,
		},
	}

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err := di.DialAndSend(m)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	require.Len(t, h.RequestDocuments, len(expFiles)*len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Text body\n" +
			"\n" +
			"Attachments:\n" +
			"- 🔗 inline.jpg (image/jpeg) 3B, sending...\n" +
			"- 📎 hey.txt (text/plain) 2B, sending...\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, sending..."
	require.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		require.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLargeMessageAggressivelyTruncated(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.MessageLengthToSendAsFile = 12
	telegramConfig.ForwardedAttachmentMaxSize = 1024
	telegramConfig.ForwardedAttachmentMaxPhotoSize = 1024
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", strings.Repeat("Hello_", 60))

	expFull :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			strings.Repeat("Hello_", 60)
	expFiles := []*FormattedAttachment{
		{
			Filename: "full_message.txt",
			Caption:  "Full message",
			Content:  []byte(expFull),
			FileType: AttachmentTypeDocument,
		},
	}

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err := di.DialAndSend(m)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	require.Len(t, h.RequestDocuments, len(strings.Split(telegramConfig.ChatIDs, ",")))

	exp :=
		"From: from@t"
	require.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		require.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLargeMessageProperlyTruncated(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.MessageLengthToSendAsFile = 100
	telegramConfig.ForwardedAttachmentMaxSize = 1024
	telegramConfig.ForwardedAttachmentMaxPhotoSize = 1024
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", strings.Repeat("Hello_", 60))

	expFull :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			strings.Repeat("Hello_", 60)
	expFiles := []*FormattedAttachment{
		{
			Filename: "full_message.txt",
			Caption:  "Full message",
			Content:  []byte(expFull),
			FileType: AttachmentTypeDocument,
		},
	}

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err := di.DialAndSend(m)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	require.Len(t, h.RequestDocuments, len(strings.Split(telegramConfig.ChatIDs, ",")))

	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Hello_Hello_Hello_Hello_Hello_Hello_He\n" +
			"\n" +
			"[truncated]"
	require.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		require.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLargeMessageWithAttachmentsProperlyTruncated(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.MessageLengthToSendAsFile = 150
	telegramConfig.ForwardedAttachmentMaxSize = 1024
	telegramConfig.ForwardedAttachmentMaxPhotoSize = 1024
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Test subj")
	m.SetBody("text/plain", strings.Repeat("Hel lo", 60))
	m.Attach("attachment.jpg", goMailBody([]byte("JPG")))

	expFull :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			strings.Repeat("Hel lo", 60) +
			"\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, sending..."
	expFiles := []*FormattedAttachment{
		{
			Filename: "full_message.txt",
			Caption:  "Full message",
			Content:  []byte(expFull),
			FileType: AttachmentTypeDocument,
		},
		{
			Filename: "attachment.jpg",
			Caption:  "attachment.jpg",
			Content:  []byte("JPG"),
			FileType: AttachmentTypePhoto,
		},
	}

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err := di.DialAndSend(m)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	require.Len(t, h.RequestDocuments, 2*len(strings.Split(telegramConfig.ChatIDs, ",")))

	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Test subj\n" +
			"\n" +
			"Hel loHel loHel loHel loHel\n" +
			"\n" +
			"[truncated]\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 attachment.jpg (image/jpeg) 3B, sending..."
	require.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		require.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestMuttMessagePlaintextParsing(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.ForwardedAttachmentMaxSize = 1024
	telegramConfig.ForwardedAttachmentMaxPhotoSize = 1024
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	// date | mutt -s "test" -a ./tt -- to@test
	m := `Received: from USER by HOST with local (Exim 4.92)
	(envelope-from <from@test>)
	id 111111-000000-OS
	for to@test; Sun, 29 Aug 2021 21:30:10 +0300
Date: Sun, 29 Aug 2021 21:30:10 +0300
From: from@test
To: to@test
Subject: test
Message-ID: <20210829183010.11111111@HOST>
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="TB36FDmn/VVEgNH/"
Content-Disposition: inline
User-Agent: Mutt/1.10.1 (2018-07-13)


--TB36FDmn/VVEgNH/
Content-Type: text/plain; charset=us-ascii
Content-Disposition: inline

Sun 29 Aug 2021 09:30:10 PM MSK

--TB36FDmn/VVEgNH/
Content-Type: text/plain; charset=us-ascii
Content-Disposition: attachment; filename=tt

hoho

--TB36FDmn/VVEgNH/--
.`

	expFiles := []*FormattedAttachment{
		{
			Filename: "tt",
			Caption:  "tt",
			Content:  []byte("hoho\n"),
			FileType: AttachmentTypeDocument,
		},
	}

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	ds, err := di.Dial()
	require.NoError(t, err)
	defer func() { _ = ds.Close() }()
	err = ds.Send("from@test", []string{"to@test"}, bytes.NewBufferString(m))
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	require.Len(t, h.RequestDocuments, len(expFiles)*len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: test\n" +
			"\n" +
			"Sun 29 Aug 2021 09:30:10 PM MSK\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 tt (text/plain) 5B, sending..."
	require.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		require.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestMailxMessagePlaintextParsing(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	telegramConfig.ForwardedAttachmentMaxSize = 1024
	telegramConfig.ForwardedAttachmentMaxPhotoSize = 1024
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	// date | mail -A ./tt -s "test" to@test
	m := `Received: from USER by HOST with local (Exim 4.92)
	(envelope-from <from@test>)
	id 111111-000000-Bj
	for to@test; Sun, 29 Aug 2021 21:30:23 +0300
MIME-Version: 1.0
Content-Type: multipart/mixed; boundary="1493203554-1630261823=:345292"
Subject: test
To: to@test
X-Mailer: mail (GNU Mailutils 3.5)
Message-Id: <2222222-000000-Bj@HOST>
From: from@test
Date: Sun, 29 Aug 2021 21:30:23 +0300

--1493203554-1630261823=:345292
Content-Type: text/plain; charset=UTF-8
Content-Disposition: attachment
Content-Transfer-Encoding: 8bit
Content-ID: <20210829213023.345292.1@HOST>

Sun 29 Aug 2021 09:30:23 PM MSK

--1493203554-1630261823=:345292
Content-Type: application/octet-stream; name="tt"
Content-Disposition: attachment; filename="./tt"
Content-Transfer-Encoding: base64
Content-ID: <20210829213023.345292.1@HOST>

aG9obwo=
--1493203554-1630261823=:345292--
.`

	expFiles := []*FormattedAttachment{
		{
			Filename: "tt",
			Caption:  "./tt",
			Content:  []byte("hoho\n"),
			FileType: AttachmentTypeDocument,
		},
	}

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	ds, err := di.Dial()
	require.NoError(t, err)
	defer func() { _ = ds.Close() }()
	err = ds.Send("from@test", []string{"to@test"}, bytes.NewBufferString(m))
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	require.Len(t, h.RequestDocuments, len(expFiles)*len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: test\n" +
			"\n" +
			"Sun 29 Aug 2021 09:30:23 PM MSK\n" +
			"\n" +
			"Attachments:\n" +
			"- 📎 ./tt (application/octet-stream) 5B, sending..."
	require.Equal(t, exp, h.RequestMessages[0])
	for i, expDoc := range expFiles {
		require.Equal(t, expDoc, h.RequestDocuments[i])
	}
}

func TestLatin1Encoding(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	// https://github.com/KostyaEsmukov/smtp_to_telegram/issues/24#issuecomment-980684254
	m := `Date: Sat, 27 Nov 2021 17:31:21 +0100
From: qBittorrent_notification@example.com
Subject: =?ISO-8859-1?Q?Anna-V=E9ronique?=
To: to@test
MIME-Version: 1.0
Content-Type: text/plain; charset=ISO-8859-1
Content-Transfer-Encoding: base64

QW5uYS1W6XJvbmlxdWUK
`
	err := smtp.SendMail(smtpConfig.Listen, nil, "from@test", []string{"to@test"}, []byte(m))
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
	exp :=
		"From: from@test\n" +
			"To: to@test\n" +
			"Subject: Anna-Véronique\n" +
			"\n" +
			"Anna-Véronique"
	require.Equal(t, exp, h.RequestMessages[0])
}

func HTTPServer(t *testing.T, handler http.Handler) *http.Server {
	t.Helper()
	h := &http.Server{Addr: testHTTPServerListen, Handler: handler}
	ln, err := net.Listen("tcp", h.Addr)
	if err != nil {
		t.Fatalf("failed to start HTTP server: %v", err)
	}
	go func() {
		if err := h.Serve(ln); err != nil {
			logger.Error(err)
		}
	}()
	return h
}

type SuccessHandler struct {
	RequestMessages  []string
	RequestDocuments []*FormattedAttachment
}

func NewSuccessHandler() *SuccessHandler {
	return &SuccessHandler{
		RequestMessages:  []string{},
		RequestDocuments: []*FormattedAttachment{},
	}
}

func (s *SuccessHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "sendMessage") {
		if _, err := w.Write([]byte(`{"ok":true,"result":{"message_id": 123123}}`)); err != nil {
			panic(fmt.Errorf("failed to write response: %w", err))
		}
		if err := r.ParseForm(); err != nil {
			panic(err)
		}
		s.RequestMessages = append(s.RequestMessages, r.PostForm.Get("text"))
		return
	}
	isSendDocument := strings.Contains(r.URL.Path, "sendDocument")
	isSendPhoto := strings.Contains(r.URL.Path, "sendPhoto")
	if isSendDocument || isSendPhoto {
		if _, err := w.Write([]byte(`{}`)); err != nil {
			panic(fmt.Errorf("failed to write response: %w", err))
		}
		if r.FormValue("reply_to_message_id") != "123123" {
			panic(fmt.Errorf("%w: unexpected reply_to_message_id: %s", errTestUnexpectedValue, r.FormValue("reply_to_message_id")))
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			panic(err)
		}
		key := "document"
		fileType := AttachmentTypeDocument
		if isSendPhoto {
			key = "photo"
			fileType = AttachmentTypePhoto
		}
		file, header, err := r.FormFile(key)
		if err != nil {
			panic(err)
		}
		defer func() {
			if err := file.Close(); err != nil {
				panic(fmt.Errorf("failed to close file: %w", err))
			}
		}()
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, file); err != nil {
			panic(fmt.Errorf("failed to copy file Content: %w", err))
		}
		s.RequestDocuments = append(
			s.RequestDocuments,
			&FormattedAttachment{
				Filename: header.Filename,
				Caption:  r.FormValue("caption"),
				Content:  buf.Bytes(),
				FileType: fileType,
			},
		)
	} else {
		w.WriteHeader(404)
		if _, err := w.Write([]byte("Error")); err != nil {
			panic(fmt.Errorf("failed to write error response: %w", err))
		}
	}
}

type ErrorHandler struct{}

func (s *ErrorHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(400)
	if _, err := w.Write([]byte("Error")); err != nil {
		panic(fmt.Errorf("failed to write error response: %w", err))
	}
}

func TestLoadFilterRules(t *testing.T) {
	// Create a temporary filter rules file
	tmpfile, err := os.CreateTemp("", "filter_rules_test*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-spam
    conditions:
      - field: subject
        pattern: 'spam'

  - name: block-domain
    match: all
    conditions:
      - field: from
        pattern: '@spammer\.com$'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)
	require.Len(t, filterRules, 2)
	require.Equal(t, "block-spam", filterRules[0].Name)
	require.Equal(t, "all", filterRules[0].Match) // default
	require.Equal(t, "block-domain", filterRules[1].Name)
}

func TestLoadFilterRulesEmptyFilename(t *testing.T) {
	err := loadFilterRules("")
	require.NoError(t, err)
	require.Nil(t, filterRules)
}

func TestLoadFilterRulesNonExistentFile(t *testing.T) {
	err := loadFilterRules("/non/existent/file.yaml")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read config file")
}

func TestLoadFilterRulesInvalidRegex(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_invalid*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: bad-regex
    conditions:
      - field: subject
        pattern: '[invalid('

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid regex pattern")
}

func TestLoadFilterRulesInvalidField(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_invalid_field*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: bad-field
    conditions:
      - field: subjekt
        pattern: 'test'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid field 'subjekt'")
}

func TestLoadFilterRulesInvalidMatchType(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_invalid_match*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: bad-match
    match: invalid
    conditions:
      - field: subject
        pattern: 'test'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid match type")
}

func TestFilterRulesMatchAll(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_match_all*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-dating-spam
    match: all
    conditions:
      - field: from
        pattern: '@ecinetworks\.com$'
      - field: subject
        pattern: 'get(ting)? to know'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// Both conditions match - should reject (case-insensitive)
	rejected, ruleName := checkFilterRules("sender@ecinetworks.com", "to@test.com", "Getting to know you", "body", "")
	require.True(t, rejected)
	require.Equal(t, "block-dating-spam", ruleName)

	// Only from matches - should not reject
	rejected, _ = checkFilterRules("sender@ecinetworks.com", "to@test.com", "Hello", "body", "")
	require.False(t, rejected)

	// Only subject matches - should not reject
	rejected, _ = checkFilterRules("sender@other.com", "to@test.com", "Getting to know you", "body", "")
	require.False(t, rejected)

	// Neither matches - should not reject
	rejected, _ = checkFilterRules("sender@other.com", "to@test.com", "Hello", "body", "")
	require.False(t, rejected)
}

func TestFilterRulesMatchAny(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_match_any*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-spam-domains
    match: any
    conditions:
      - field: body
        pattern: 'cdnex\.online'
      - field: body
        pattern: 'spam-tracker\.net'
      - field: body
        pattern: 'click-now\.xyz'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// First condition matches - should reject
	rejected, ruleName := checkFilterRules("from@test.com", "to@test.com", "subject", "Visit cdnex.online", "")
	require.True(t, rejected)
	require.Equal(t, "block-spam-domains", ruleName)

	// Second condition matches - should reject
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "subject", "Check spam-tracker.net", "")
	require.True(t, rejected)

	// Third condition matches - should reject
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "subject", "Click click-now.xyz", "")
	require.True(t, rejected)

	// None match - should not reject
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "subject", "Clean body text", "")
	require.False(t, rejected)
}

func TestFilterRulesFieldMatching(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_fields*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-from
    conditions:
      - field: from
        pattern: 'blocked@'

  - name: block-to
    conditions:
      - field: to
        pattern: 'blocked-recipient@'

  - name: block-subject
    conditions:
      - field: subject
        pattern: 'BLOCKED_SUBJECT'

  - name: block-body
    conditions:
      - field: body
        pattern: 'BLOCKED_BODY'

  - name: block-html
    conditions:
      - field: html
        pattern: 'BLOCKED_HTML'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// Test from field
	rejected, ruleName := checkFilterRules("blocked@example.com", "to@test.com", "subject", "body", "html")
	require.True(t, rejected)
	require.Equal(t, "block-from", ruleName)

	// Test to field
	rejected, ruleName = checkFilterRules("from@test.com", "blocked-recipient@test.com", "subject", "body", "html")
	require.True(t, rejected)
	require.Equal(t, "block-to", ruleName)

	// Test subject field
	rejected, ruleName = checkFilterRules("from@test.com", "to@test.com", "BLOCKED_SUBJECT here", "body", "html")
	require.True(t, rejected)
	require.Equal(t, "block-subject", ruleName)

	// Test body field
	rejected, ruleName = checkFilterRules("from@test.com", "to@test.com", "subject", "Contains BLOCKED_BODY", "html")
	require.True(t, rejected)
	require.Equal(t, "block-body", ruleName)

	// Test html field
	rejected, ruleName = checkFilterRules("from@test.com", "to@test.com", "subject", "body", "<p>BLOCKED_HTML</p>")
	require.True(t, rejected)
	require.Equal(t, "block-html", ruleName)

	// Test no match
	rejected, _ = checkFilterRules("good@example.com", "good@test.com", "good subject", "good body", "good html")
	require.False(t, rejected)
}

func TestFilterRulesBodyOrHtml(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_body_or_html*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-tracking-url
    conditions:
      - field: body_or_html
        pattern: 'adnxs\.com'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// Pattern in body only - should reject
	rejected, ruleName := checkFilterRules("from@test.com", "to@test.com", "subject", "Visit adnxs.com", "")
	require.True(t, rejected)
	require.Equal(t, "block-tracking-url", ruleName)

	// Pattern in html only - should reject
	rejected, ruleName = checkFilterRules("from@test.com", "to@test.com", "subject", "", "<a href='http://adnxs.com'>link</a>")
	require.True(t, rejected)
	require.Equal(t, "block-tracking-url", ruleName)

	// Pattern in both - should reject
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "subject", "adnxs.com", "<a href='adnxs.com'>link</a>")
	require.True(t, rejected)

	// Pattern in neither - should not reject
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "subject", "clean body", "<p>clean html</p>")
	require.False(t, rejected)
}

func TestFilterRulesHtmlOnlyEmail(t *testing.T) {
	// Test that URLs in HTML-only emails (where body might be empty) are caught
	tmpfile, err := os.CreateTemp("", "filter_rules_html_only*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-html-url
    conditions:
      - field: html
        pattern: 'https?://[^\s"''<>]+\.(xyz|top|click)'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// URL only in HTML - should reject
	rejected, _ := checkFilterRules("from@test.com", "to@test.com", "subject", "", "<a href='http://spam.xyz/click'>Click here</a>")
	require.True(t, rejected)

	// Clean HTML - should not reject
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "subject", "", "<p>Hello world</p>")
	require.False(t, rejected)
}

func TestFilterRulesFirstMatchWins(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_first_match*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: first-rule
    conditions:
      - field: subject
        pattern: 'test'

  - name: second-rule
    conditions:
      - field: subject
        pattern: 'test subject'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// Both rules would match, but first wins
	rejected, ruleName := checkFilterRules("from@test.com", "to@test.com", "test subject", "body", "")
	require.True(t, rejected)
	require.Equal(t, "first-rule", ruleName)
}

func TestFilterRulesCaseInsensitive(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_case_insensitive*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-spam
    conditions:
      - field: subject
        pattern: 'spam'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// Pattern is lowercase, but should match uppercase
	rejected, _ := checkFilterRules("from@test.com", "to@test.com", "SPAM MESSAGE", "body", "")
	require.True(t, rejected)

	// Mixed case should also match
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "SpAm MeSsAgE", "body", "")
	require.True(t, rejected)

	// Lowercase should match too
	rejected, _ = checkFilterRules("from@test.com", "to@test.com", "spam message", "body", "")
	require.True(t, rejected)
}

func TestFilterRulesNoRulesLoaded(t *testing.T) {
	filterRules = nil

	rejected, _ := checkFilterRules("any@email.com", "to@test.com", "any subject", "any body", "any html")
	require.False(t, rejected)
}

func TestFilterRulesEmptyConditions(t *testing.T) {
	tmpfile, err := os.CreateTemp("", "filter_rules_empty_conditions*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: empty-rule
    conditions: []

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	err = loadFilterRules(tmpfile.Name())
	require.NoError(t, err)

	// Empty conditions should not match
	rejected, _ := checkFilterRules("any@email.com", "to@test.com", "any subject", "any body", "")
	require.False(t, rejected)
}

func TestSMTPStartWithNonExistentFilterRulesFile(t *testing.T) {
	smtpConfig := makeSMTPConfig()
	smtpConfig.ConfigFile = "/non/existent/filter_rules.yaml"
	telegramConfig := makeTelegramConfig()

	_, err := SMTPStart(smtpConfig, telegramConfig)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to load config")
}

func TestFilteredEmailReturns554(t *testing.T) {
	// Create a temporary filter rules file
	tmpfile, err := os.CreateTemp("", "filter_smtp_test*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-spam-subject
    conditions:
      - field: subject
        pattern: 'spam test'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	smtpConfig := makeSMTPConfig()
	smtpConfig.ConfigFile = tmpfile.Name()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	// Test filtered email returns 554 (case-insensitive)
	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "SPAM TEST message")
	m.SetBody("text/plain", "This is a test body")

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err = di.DialAndSend(m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "554")
	require.Contains(t, err.Error(), "email rejected by filter rule")
}

func TestFilteredEmailWithBodyPattern(t *testing.T) {
	// Create a temporary filter rules file
	tmpfile, err := os.CreateTemp("", "filter_body_test*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-tracking
    conditions:
      - field: body
        pattern: 'adnxs\.com'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	smtpConfig := makeSMTPConfig()
	smtpConfig.ConfigFile = tmpfile.Name()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	// Test filtered email returns 554
	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Normal subject")
	m.SetBody("text/plain", "Click here: http://adnxs.com/track")

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err = di.DialAndSend(m)
	require.Error(t, err)
	require.Contains(t, err.Error(), "554")
	require.Contains(t, err.Error(), "email rejected by filter rule")
}

func TestNonFilteredEmailPasses(t *testing.T) {
	// Create a temporary filter rules file
	tmpfile, err := os.CreateTemp("", "filter_pass_test*.yaml")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpfile.Name()) }()

	content := `filter_rules:
  - name: block-spam
    conditions:
      - field: subject
        pattern: 'BLOCKED_PATTERN'

`
	_, err = tmpfile.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, tmpfile.Close())

	smtpConfig := makeSMTPConfig()
	smtpConfig.ConfigFile = tmpfile.Name()
	telegramConfig := makeTelegramConfig()
	d := startSMTP(t, smtpConfig, telegramConfig)
	defer d.Shutdown()

	h := NewSuccessHandler()
	s := HTTPServer(t, h)
	defer func() { _ = s.Shutdown(context.Background()) }()

	// Test non-filtered email passes through
	m := gomail.NewMessage()
	m.SetHeader("From", "from@test")
	m.SetHeader("To", "to@test")
	m.SetHeader("Subject", "Normal subject")
	m.SetBody("text/plain", "Normal body")

	di := gomail.NewDialer(testSMTPListenHost, testSMTPListenPort, "", "")
	err = di.DialAndSend(m)
	require.NoError(t, err)

	require.Len(t, h.RequestMessages, len(strings.Split(telegramConfig.ChatIDs, ",")))
}
