package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

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

var (
	errMissingFromHeader = errors.New("missing From header in message")
	errGetMeNotOk        = errors.New("getMe returned not ok")
	errGetMeRetries      = errors.New("getMe failed after retries")
	errGetUpdatesNotOk   = errors.New("getUpdates returned not ok")
	errNoOwnAddress      = errors.New("no To/CC address matches allowed hosts")
)

// ParseMessageHeaders extracts email-style headers from the top of a Telegram message.
func ParseMessageHeaders(text string) (ParsedHeaders, error) {
	var headers ParsedHeaders
	// Only scan lines before the first blank line (header section)
	for line := range strings.SplitSeq(text, "\n") {
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
// It picks the first To or CC address whose domain matches an allowed host as the
// sender, so the reply comes from our own address even when we were CC'd.
func ComposeReplyAddresses(headers *ParsedHeaders, allowedHosts []string) (from string, to, cc []string, subject string, err error) {
	toAddresses := splitAddresses(headers.To)
	ccAddresses := splitAddresses(headers.CC)
	allAddresses := slices.Concat(toAddresses, ccAddresses)

	from = findOwnAddress(allAddresses, allowedHosts)
	if from == "" {
		return "", nil, nil, "", errNoOwnAddress
	}

	if headers.ReplyTo != "" {
		to = []string{headers.ReplyTo}
	} else {
		to = []string{headers.From}
	}

	for _, addr := range allAddresses {
		trimmed := strings.TrimSpace(addr)
		if trimmed != "" && trimmed != from && !slices.Contains(to, trimmed) && !slices.Contains(cc, trimmed) {
			cc = append(cc, trimmed)
		}
	}

	subject = headers.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	return from, to, cc, subject, nil
}

func parseChatIDs(chatIDsStr string) ([]int64, error) {
	var ids []int64
	for part := range strings.SplitSeq(chatIDsStr, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid chat ID %q: %w", s, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func splitAddresses(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(s)
	if err != nil {
		// Fall back to simple comma split for non-RFC addresses
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
	result := make([]string, 0, len(addrs))
	for _, a := range addrs {
		result = append(result, a.Address)
	}
	return result
}

// findOwnAddress returns the first address whose domain matches one of the
// allowed hosts. If allowedHosts contains "." (any host), it returns the first
// address unconditionally.
func findOwnAddress(addresses, allowedHosts []string) string {
	anyHost := slices.Contains(allowedHosts, ".")
	for _, addr := range addresses {
		addr = strings.TrimSpace(addr)
		if addr == "" {
			continue
		}
		if anyHost {
			return addr
		}
		if _, domain, ok := strings.Cut(addr, "@"); ok {
			for _, host := range allowedHosts {
				if strings.EqualFold(host, domain) {
					return addr
				}
			}
		}
	}
	return ""
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
func HandleTelegramReply(update TelegramUpdate, smtpOutConfig *SMTPOutConfig, botUserID int64, allowedHosts []string) string {
	msg := update.Message
	if msg == nil || msg.ReplyToMessage == nil {
		return ""
	}

	// Only handle replies to our own messages — silently ignore others
	if msg.ReplyToMessage.From == nil || msg.ReplyToMessage.From.ID != botUserID {
		return ""
	}

	if !smtpOutConfig.IsConfigured() {
		return "Reply-to-email is not configured. Set ST_SMTP_OUT_HOST to enable."
	}

	headers, err := ParseMessageHeaders(msg.ReplyToMessage.Text)
	if err != nil {
		return "Could not parse the original email from the message."
	}

	from, to, cc, subject, err := ComposeReplyAddresses(&headers, allowedHosts)
	if err != nil {
		return "Could not determine sender address from the original email."
	}
	if err := SendReplyEmail(smtpOutConfig, from, to, cc, subject, msg.Text); err != nil {
		return fmt.Sprintf("Failed to send email: %s", err)
	}

	allRecipients := slices.Concat(to, cc)
	return fmt.Sprintf("Email sent from %s to %s", from, strings.Join(allRecipients, ", "))
}

func GetBotUserID(ctx context.Context, telegramConfig *TelegramConfig, client *http.Client) (int64, error) {
	apiURL := fmt.Sprintf("%sbot%s/getMe", telegramConfig.APIPrefix, telegramConfig.BotToken)
	maxRetries := 5
	backoff := 2 * time.Second

	for attempt := range maxRetries {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, http.NoBody)
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

		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			logger.Warningf("getMe attempt %d/%d: HTTP %d", attempt+1, maxRetries, resp.StatusCode)
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
			return 0, errGetMeNotOk
		}
		return result.Result.ID, nil
	}
	return 0, fmt.Errorf("%w: %d attempts", errGetMeRetries, maxRetries)
}

func getUpdates(ctx context.Context, telegramConfig *TelegramConfig, client *http.Client, offset int) ([]TelegramUpdate, error) {
	apiURL := fmt.Sprintf("%sbot%s/getUpdates", telegramConfig.APIPrefix, telegramConfig.BotToken)
	params := url.Values{
		"timeout":         {"30"},
		"offset":          {fmt.Sprintf("%d", offset)},
		"allowed_updates": {`["message"]`},
	}
	fullURL := apiURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, http.NoBody)
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: HTTP %d", errGetUpdatesNotOk, resp.StatusCode)
	}

	var result TelegramGetUpdatesResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.Ok {
		return nil, errGetUpdatesNotOk
	}
	return result.Result, nil
}

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
		// Drain remaining body so the HTTP transport can reuse the connection.
		_, _ = io.Copy(io.Discard, resp.Body)
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Warningf("Failed to close response body: %v", closeErr)
		}
	}()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		logger.Errorf("Notification failed: (%d) %s", resp.StatusCode, SanitizeBotToken(string(body), telegramConfig.BotToken))
	}
}

func PollTelegramUpdates(
	ctx context.Context,
	telegramConfig *TelegramConfig,
	smtpOutConfig *SMTPOutConfig,
	allowedChatIDs []int64,
	allowedHosts []string,
) {
	client := &http.Client{Timeout: 40 * time.Second}

	botUserID, err := GetBotUserID(ctx, telegramConfig, client)
	if err != nil {
		logger.Errorf("Failed to get bot identity, reply feature disabled: %s", SanitizeBotToken(err.Error(), telegramConfig.BotToken))
		return
	}
	logger.Infof("Bot user ID: %d, starting Telegram polling", botUserID)

	// Flush stale updates on cold start
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
			select {
			case <-ctx.Done():
				logger.Info("Telegram polling stopped during backoff")
				return
			case <-time.After(errorBackoff):
			}
			errorBackoff = min(errorBackoff*2, maxBackoff)
			continue
		}
		errorBackoff = 5 * time.Second // reset on success

		for _, update := range updates {
			offset = update.UpdateID + 1
			if update.Message == nil {
				continue
			}
			if !slices.Contains(allowedChatIDs, update.Message.Chat.ID) {
				continue // ignore updates from unauthorized chats
			}
			notification := HandleTelegramReply(update, smtpOutConfig, botUserID, allowedHosts)
			if notification != "" {
				sendNotification(ctx, telegramConfig, client, update.Message.Chat.ID, update.Message.MessageID, notification)
			}
		}
	}
}
