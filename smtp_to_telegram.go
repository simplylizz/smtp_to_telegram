package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/docker/go-units"
	"github.com/jhillyerd/enmime/v2"
	"github.com/phires/go-guerrilla"
	"github.com/phires/go-guerrilla/backends"
	"github.com/phires/go-guerrilla/log"
	"github.com/phires/go-guerrilla/mail"
	"github.com/urfave/cli/v3"
	"gopkg.in/yaml.v3"
)

var (
	Version     = "UNKNOWN_RELEASE"
	logger      log.Logger
	filterRules []FilterRule

	// Sentinel errors
	errInvalidMatchType       = errors.New("invalid match type")
	errInvalidField           = errors.New("invalid field")
	errRejectedByFilter       = errors.New("email rejected by filter rule")
	errReadingJSON            = errors.New("error reading json body of sendMessage")
	errParsingJSON            = errors.New("error parsing json body of sendMessage")
	errResponseNotOK          = errors.New("telegram API response not ok")
	errUnknownFileType        = errors.New("unknown file type")
	errEmailParsing           = errors.New("error occurred during email parsing")
	errMessageTooLarge        = errors.New("message length is larger than forwarded-attachment-max-size")
	errUnexpectedTruncation   = errors.New("unexpected length of truncated message")
	errTelegramNon200         = errors.New("non-200 response from Telegram")
	errSanitizedTelegramFail  = errors.New("telegram operation failed")
	errBlacklistFileDeprecate = errors.New("--blacklist-file is deprecated, use --config-file with filter_rules in YAML instead")
)

type FilterCondition struct {
	Field   string         `yaml:"field"`
	Pattern string         `yaml:"pattern"`
	regex   *regexp.Regexp // compiled pattern
}

type FilterRule struct {
	Name       string            `yaml:"name"`
	Match      string            `yaml:"match"` // "all" or "any"
	Conditions []FilterCondition `yaml:"conditions"`
}

type FilterConfig struct {
	FilterRules []FilterRule `yaml:"filter_rules"`
}

const (
	BodyTruncated = "\n\n[truncated]"
)

type SMTPConfig struct {
	Listen          string
	PrimaryHost     string
	MaxEnvelopeSize int64
	AllowedHosts    string
	ConfigFile      string
}

type TelegramConfig struct {
	ChatIDs                      string
	BotToken                     string
	APIPrefix                    string
	APITimeoutSeconds            float64
	MessageTemplate              string
	ForwardedAttachmentMaxSize      int
	ForwardedAttachmentMaxPhotoSize int
	ForwardedAttachmentRespectErrors bool
	MessageLengthToSendAsFile    uint
}

type TelegramAPIMessageResult struct {
	Ok     bool                `json:"ok"`
	Result *TelegramAPIMessage `json:"result"`
}

type TelegramAPIMessage struct {
	// https://core.telegram.org/bots/api#message
	MessageID json.Number `json:"message_id"`
}

type FormattedEmail struct {
	From        string
	To          string
	Subject     string
	Text        string
	HTML        string
	Attachments []*FormattedAttachment
}

const (
	AttachmentTypeDocument = iota
	AttachmentTypePhoto
)

type FormattedAttachment struct {
	Filename string
	Caption  string
	Content  []byte
	FileType int
}

func GetHostname() (string, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", fmt.Errorf("unable to detect hostname: %w", err)
	}
	return hostname, nil
}

func main() {
	cmd := &cli.Command{
		Name: "smtp_to_telegram",
		Usage: "A small program which listens for SMTP and sends " +
			"all incoming Email messages to Telegram.",
		Version: Version,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			smtpMaxEnvelopeSize, err := units.FromHumanSize(cmd.String("smtp-max-envelope-size"))
			if err != nil {
				return err
			}
			if cmd.String("blacklist-file") != "" {
				return errBlacklistFileDeprecate
			}
			smtpPrimaryHost := cmd.String("smtp-primary-host")
			if smtpPrimaryHost == "" {
				smtpPrimaryHost, err = GetHostname()
				if err != nil {
					return err
				}
			}
			smtpConfig := &SMTPConfig{
				Listen:          cmd.String("smtp-listen"),
				PrimaryHost:     smtpPrimaryHost,
				MaxEnvelopeSize: smtpMaxEnvelopeSize,
				AllowedHosts:    cmd.String("smtp-allowed-hosts"),
				ConfigFile:      cmd.String("config-file"),
			}
			forwardedAttachmentMaxSize, err := units.FromHumanSize(cmd.String("forwarded-attachment-max-size"))
			if err != nil {
				return err
			}
			forwardedAttachmentMaxPhotoSize, err := units.FromHumanSize(cmd.String("forwarded-attachment-max-photo-size"))
			if err != nil {
				return err
			}
			telegramConfig := &TelegramConfig{
				ChatIDs:                   cmd.String("telegram-chat-ids"),
				BotToken:                  cmd.String("telegram-bot-token"),
				APIPrefix:                 cmd.String("telegram-api-prefix"),
				APITimeoutSeconds:         cmd.Float64("telegram-api-timeout-seconds"),
				MessageTemplate:           cmd.String("message-template"),
				ForwardedAttachmentMaxSize:         int(forwardedAttachmentMaxSize),
				ForwardedAttachmentMaxPhotoSize:    int(forwardedAttachmentMaxPhotoSize),
				ForwardedAttachmentRespectErrors:   cmd.Bool("forwarded-attachment-respect-errors"),
				MessageLengthToSendAsFile: cmd.Uint("message-length-to-send-as-file"),
			}
			d, err := SMTPStart(smtpConfig, telegramConfig)
			if err != nil {
				return fmt.Errorf("start error: %w", err)
			}
			return awaitShutdown(ctx, &d)
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "smtp-listen",
				Value:   "127.0.0.1:2525",
				Usage:   "SMTP: TCP address to listen to",
				Sources: cli.EnvVars("ST_SMTP_LISTEN"),
			},
			&cli.StringFlag{
				Name:    "smtp-primary-host",
				Usage:   "SMTP: primary host (defaults to system hostname)",
				Sources: cli.EnvVars("ST_SMTP_PRIMARY_HOST"),
			},
			&cli.StringFlag{
				Name:    "smtp-allowed-hosts",
				Usage:   "SMTP: allowed hosts separated by comma, default is any",
				Value:   ".",
				Sources: cli.EnvVars("ST_SMTP_ALLOWED_HOSTS"),
			},
			&cli.StringFlag{
				Name:    "smtp-max-envelope-size",
				Usage:   "Max size of an incoming Email. Examples: 5k, 10m.",
				Value:   "50m",
				Sources: cli.EnvVars("ST_SMTP_MAX_ENVELOPE_SIZE"),
			},
			&cli.StringFlag{
				Name:    "blacklist-file",
				Usage:   "DEPRECATED: Use --config-file instead",
				Sources: cli.EnvVars("ST_BLACKLIST_FILE"),
				Hidden:  true,
			},
			&cli.StringFlag{
				Name:    "config-file",
				Usage:   "Path to YAML configuration file",
				Sources: cli.EnvVars("ST_CONFIG_FILE"),
			},
			&cli.StringFlag{
				Name:     "telegram-chat-ids",
				Usage:    "Telegram: comma-separated list of chat ids",
				Sources:  cli.EnvVars("ST_TELEGRAM_CHAT_IDS"),
				Required: true,
			},
			&cli.StringFlag{
				Name:     "telegram-bot-token",
				Usage:    "Telegram: bot token",
				Sources:  cli.EnvVars("ST_TELEGRAM_BOT_TOKEN"),
				Required: true,
			},
			&cli.StringFlag{
				Name:    "telegram-api-prefix",
				Usage:   "Telegram: API url prefix",
				Value:   "https://api.telegram.org/",
				Sources: cli.EnvVars("ST_TELEGRAM_API_PREFIX"),
			},
			&cli.StringFlag{
				Name:    "message-template",
				Usage:   "Telegram message template",
				Value:   "From: {from}\\nTo: {to}\\nSubject: {subject}\\n\\n{body}\\n\\n{attachments_details}",
				Sources: cli.EnvVars("ST_TELEGRAM_MESSAGE_TEMPLATE"),
			},
			&cli.Float64Flag{
				Name:    "telegram-api-timeout-seconds",
				Usage:   "HTTP timeout used for requests to the Telegram API",
				Value:   30,
				Sources: cli.EnvVars("ST_TELEGRAM_API_TIMEOUT_SECONDS"),
			},
			&cli.StringFlag{
				Name: "forwarded-attachment-max-size",
				Usage: "Max size of an attachment to be forwarded to telegram. " +
					"0 -- disable forwarding. Examples: 5k, 10m. " +
					"Telegram API has a 50m limit on their side.",
				Value:   "10m",
				Sources: cli.EnvVars("ST_FORWARDED_ATTACHMENT_MAX_SIZE"),
			},
			&cli.StringFlag{
				Name: "forwarded-attachment-max-photo-size",
				Usage: "Max size of a photo attachment to be forwarded to telegram. " +
					"0 -- disable forwarding. Examples: 5k, 10m. " +
					"Telegram API has a 10m limit on their side.",
				Value:   "10m",
				Sources: cli.EnvVars("ST_FORWARDED_ATTACHMENT_MAX_PHOTO_SIZE"),
			},
			&cli.BoolFlag{
				Name: "forwarded-attachment-respect-errors",
				Usage: "Reject the whole email if some attachments " +
					"could not have been forwarded",
				Value:   false,
				Sources: cli.EnvVars("ST_FORWARDED_ATTACHMENT_RESPECT_ERRORS"),
			},
			&cli.UintFlag{
				Name: "message-length-to-send-as-file",
				Usage: "If message length is greater than this number, it is " +
					"sent truncated followed by a text file containing " +
					"the full message. Telegram API has a limit of 4096 chars per message. " +
					"The maximum text file size is determined by `forwarded-attachment-max-size`.",
				Value:   4095,
				Sources: cli.EnvVars("ST_MESSAGE_LENGTH_TO_SEND_AS_FILE"),
			},
		},
	}
	err := cmd.Run(context.Background(), os.Args)
	if err != nil {
		fmt.Printf("%s\n", err)
		os.Exit(1)
	}
}

func getAllowedHosts(smtpConfig *SMTPConfig) []string {
	allowedHosts := strings.Split(smtpConfig.AllowedHosts, ",")

	if len(allowedHosts) == 1 && allowedHosts[0] == "" {
		allowedHosts[0] = "."
	}

	return allowedHosts
}

func loadFilterRules(filename string) error {
	filterRules = nil

	if filename == "" {
		return nil
	}

	data, err := os.ReadFile(filename) //nolint:gosec // User-specified config file path is intentional
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var config FilterConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config YAML: %w", err)
	}

	// Compile regex patterns
	for i := range config.FilterRules {
		rule := &config.FilterRules[i]
		// Default match to "all" if not specified
		if rule.Match == "" {
			rule.Match = "all"
		}
		if rule.Match != "all" && rule.Match != "any" {
			return fmt.Errorf("rule '%s': %w '%s' (must be 'all' or 'any')", rule.Name, errInvalidMatchType, rule.Match)
		}
		for j := range rule.Conditions {
			cond := &rule.Conditions[j]
			if !isValidFilterField(cond.Field) {
				return fmt.Errorf("rule '%s': %w '%s' (must be one of: from, to, subject, body, html, body_or_html)", rule.Name, errInvalidField, cond.Field)
			}
			// Make pattern case-insensitive
			pattern := cond.Pattern
			if !strings.HasPrefix(pattern, "(?i)") {
				pattern = "(?i)" + pattern
			}
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("rule '%s': invalid regex pattern '%s': %w", rule.Name, cond.Pattern, err)
			}
			cond.regex = compiled
		}
	}

	filterRules = config.FilterRules

	if logger != nil {
		logger.Infof("Loaded %d filter rules from %s", len(filterRules), filename)
	}
	return nil
}

func isValidFilterField(field string) bool {
	switch field {
	case "from", "to", "subject", "body", "html", "body_or_html":
		return true
	default:
		return false
	}
}

func checkFilterRules(from, to, subject, body, html string) (rejected bool, ruleName string) {
	if filterRules == nil {
		return false, ""
	}

	for _, rule := range filterRules {
		if evaluateRule(&rule, from, to, subject, body, html) {
			return true, rule.Name
		}
	}

	return false, ""
}

func evaluateRule(rule *FilterRule, from, to, subject, body, html string) bool {
	if len(rule.Conditions) == 0 {
		return false
	}

	if rule.Match == "any" {
		// OR logic: at least one condition must match
		for _, cond := range rule.Conditions {
			if evaluateCondition(&cond, from, to, subject, body, html) {
				return true
			}
		}
		return false
	}

	// Default: "all" - AND logic: all conditions must match
	for _, cond := range rule.Conditions {
		if !evaluateCondition(&cond, from, to, subject, body, html) {
			return false
		}
	}
	return true
}

func evaluateCondition(cond *FilterCondition, from, to, subject, body, html string) bool {
	var value string
	switch cond.Field {
	case "from":
		value = from
	case "to":
		value = to
	case "subject":
		value = subject
	case "body":
		value = body
	case "html":
		value = html
	case "body_or_html":
		// Match if pattern found in either body OR html
		return cond.regex.MatchString(body) || cond.regex.MatchString(html)
	default:
		return false
	}
	return cond.regex.MatchString(value)
}

func SMTPStart(
	smtpConfig *SMTPConfig,
	telegramConfig *TelegramConfig,
) (guerrilla.Daemon, error) {

	cfg := &guerrilla.AppConfig{LogFile: log.OutputStdout.String()}

	cfg.AllowedHosts = getAllowedHosts(smtpConfig)

	sc := guerrilla.ServerConfig{
		IsEnabled:       true,
		ListenInterface: smtpConfig.Listen,
		MaxSize:         smtpConfig.MaxEnvelopeSize,
	}
	cfg.Servers = append(cfg.Servers, sc)

	bcfg := backends.BackendConfig{
		"save_workers_size":  3,
		"save_process":       "HeadersParser|Header|Hasher|TelegramBot",
		"log_received_mails": true,
		"primary_mail_host":  smtpConfig.PrimaryHost,
	}
	cfg.BackendConfig = bcfg

	daemon := guerrilla.Daemon{Config: cfg}
	daemon.AddProcessor("TelegramBot", TelegramBotProcessorFactory(telegramConfig))

	logger = daemon.Log()

	if err := loadFilterRules(smtpConfig.ConfigFile); err != nil {
		return daemon, fmt.Errorf("failed to load config: %w", err)
	}

	err := daemon.Start()
	return daemon, err
}

func TelegramBotProcessorFactory(
	telegramConfig *TelegramConfig,
) func() backends.Decorator {
	return func() backends.Decorator {
		// https://github.com/phires/go-guerrilla/wiki/Backends,-configuring-and-extending

		return func(p backends.Processor) backends.Processor {
			return backends.ProcessWith(
				func(envelope *mail.Envelope, task backends.SelectTask) (backends.Result, error) {
					if task == backends.TaskSaveMail {
						err := SendEmailToTelegram(envelope, telegramConfig)
						if err != nil {
							return backends.NewResult(fmt.Sprintf("554 Error: %s", err)), err
						}
						return p.Process(envelope, task)
					}
					return p.Process(envelope, task)
				},
			)
		}
	}
}

func SendEmailToTelegram(
	envelope *mail.Envelope,
	telegramConfig *TelegramConfig,
) error {
	message, err := FormatEmail(envelope, telegramConfig)
	if err != nil {
		return err
	}

	if rejected, ruleName := checkFilterRules(message.From, message.To, message.Subject, message.Text, message.HTML); rejected {
		logger.Infof("Rejecting email: matched filter rule '%s'", ruleName)
		return fmt.Errorf("%w: %s", errRejectedByFilter, ruleName)
	}

	client := http.Client{
		Timeout: time.Duration(telegramConfig.APITimeoutSeconds * float64(time.Second)),
	}

	ctx := context.Background()
	for chatID := range strings.SplitSeq(telegramConfig.ChatIDs, ",") {
		sentMessage, err := SendMessageToChat(ctx, message, chatID, telegramConfig, &client)
		if err != nil {
			// If unable to send at least one message -- reject the whole email.
			return fmt.Errorf("%w: %s", errSanitizedTelegramFail, SanitizeBotToken(err.Error(), telegramConfig.BotToken))
		}

		for _, attachment := range message.Attachments {
			err = SendAttachmentToChat(ctx, attachment, chatID, telegramConfig, &client, sentMessage)
			if err != nil {
				sanitizedErr := fmt.Errorf("%w: %s", errSanitizedTelegramFail, SanitizeBotToken(err.Error(), telegramConfig.BotToken))
				if telegramConfig.ForwardedAttachmentRespectErrors {
					return sanitizedErr
				}
				logger.Errorf("Ignoring attachment sending error: %s", sanitizedErr)
			}
		}
	}
	return nil
}

func SendMessageToChat(
	ctx context.Context,
	message *FormattedEmail,
	chatID string,
	telegramConfig *TelegramConfig,
	client *http.Client,
) (*TelegramAPIMessage, error) {
	// The native golang's http client supports
	// http, https and socks5 proxies via HTTP_PROXY/HTTPS_PROXY env vars
	// out of the box.
	//
	// See: https://golang.org/pkg/net/http/#ProxyFromEnvironment
	apiURL := fmt.Sprintf(
		"%sbot%s/sendMessage?disable_web_page_preview=true",
		telegramConfig.APIPrefix,
		telegramConfig.BotToken,
	)
	formData := url.Values{"chat_id": {chatID}, "text": {message.Text}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Warningf("Failed to close response body: %v", closeErr)
		}
	}()
	if resp.StatusCode != 200 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Warningf("Failed to read error response body: %v", readErr)
		}
		return nil, fmt.Errorf(
			"%w: (%d) %s",
			errTelegramNon200,
			resp.StatusCode,
			EscapeMultiLine(body),
		)
	}

	j, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errReadingJSON, err)
	}
	result := &TelegramAPIMessageResult{}
	err = json.Unmarshal(j, result)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errParsingJSON, err)
	}
	if !result.Ok {
		return nil, fmt.Errorf("%w: %s", errResponseNotOK, j)
	}
	return result.Result, nil
}

func buildAttachmentForm(
	w *multipart.Writer,
	attachment *FormattedAttachment,
	chatID string,
	sentMessage *TelegramAPIMessage,
) (string, error) {
	// https://core.telegram.org/bots/api#sending-files
	var method, fileFieldName string
	switch attachment.FileType {
	case AttachmentTypeDocument:
		// https://core.telegram.org/bots/api#senddocument
		method = "sendDocument"
		fileFieldName = "document"
	case AttachmentTypePhoto:
		// https://core.telegram.org/bots/api#sendphoto
		method = "sendPhoto"
		fileFieldName = "photo"
	default:
		return "", fmt.Errorf("%w: %d", errUnknownFileType, attachment.FileType)
	}

	if err := w.WriteField("chat_id", chatID); err != nil {
		return "", fmt.Errorf("failed to write chat_id: %w", err)
	}
	if err := w.WriteField("reply_to_message_id", sentMessage.MessageID.String()); err != nil {
		return "", fmt.Errorf("failed to write reply_to_message_id: %w", err)
	}
	if err := w.WriteField("caption", attachment.Caption); err != nil {
		return "", fmt.Errorf("failed to write caption: %w", err)
	}

	// TODO maybe reuse files sent to multiple chats via file_id?
	fileWriter, err := w.CreateFormFile(fileFieldName, attachment.Filename)
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := fileWriter.Write(attachment.Content); err != nil {
		return "", fmt.Errorf("failed to write file content: %w", err)
	}

	return method, nil
}

func SendAttachmentToChat(
	ctx context.Context,
	attachment *FormattedAttachment,
	chatID string,
	telegramConfig *TelegramConfig,
	client *http.Client,
	sentMessage *TelegramAPIMessage,
) error {
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)

	method, err := buildAttachmentForm(w, attachment, chatID, sentMessage)
	if err != nil {
		return err
	}

	if err = w.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	apiURL := fmt.Sprintf(
		"%sbot%s/%s?disable_notification=true",
		telegramConfig.APIPrefix,
		telegramConfig.BotToken,
		method,
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, buf)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			logger.Warningf("Failed to close response body: %v", closeErr)
		}
	}()
	if resp.StatusCode != 200 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Warningf("Failed to read error response body: %v", readErr)
		}
		return fmt.Errorf(
			"%w: (%d) %s",
			errTelegramNon200,
			resp.StatusCode,
			EscapeMultiLine(body),
		)
	}
	return nil
}

func FormatEmail(envelope *mail.Envelope, telegramConfig *TelegramConfig) (*FormattedEmail, error) {
	reader := envelope.NewReader()
	env, err := enmime.ReadEnvelope(reader)
	if err != nil {
		return nil, fmt.Errorf("%s: %w: %w", envelope, errEmailParsing, err)
	}
	text := env.Text

	var attachmentsDetails []string
	var attachments []*FormattedAttachment

	doParts := func(emoji string, parts []*enmime.Part) {
		for _, part := range parts {
			if bytes.Equal(part.Content, []byte(env.Text)) {
				continue
			}
			if text == "" && part.ContentType == "text/plain" && part.FileName == "" {
				text = string(part.Content)
				continue
			}
			action := "discarded"
			contentType := GuessContentType(part.ContentType, part.FileName)
			if FileIsImage(contentType) && len(part.Content) <= telegramConfig.ForwardedAttachmentMaxPhotoSize {
				action = "sending..."
				attachments = append(attachments, &FormattedAttachment{
					Filename: part.FileName,
					Caption:  part.FileName,
					Content:  part.Content,
					FileType: AttachmentTypePhoto,
				})
			} else if len(part.Content) <= telegramConfig.ForwardedAttachmentMaxSize {
				action = "sending..."
				attachments = append(attachments, &FormattedAttachment{
					Filename: part.FileName,
					Caption:  part.FileName,
					Content:  part.Content,
					FileType: AttachmentTypeDocument,
				})
			}
			line := fmt.Sprintf(
				"- %s %s (%s) %s, %s",
				emoji,
				part.FileName,
				contentType,
				units.HumanSize(float64(len(part.Content))),
				action,
			)
			attachmentsDetails = append(attachmentsDetails, line)
		}
	}
	doParts("🔗", env.Inlines)
	doParts("📎", env.Attachments)
	for _, part := range env.OtherParts {
		line := fmt.Sprintf(
			"- ❔ %s (%s) %s, discarded",
			part.FileName,
			GuessContentType(part.ContentType, part.FileName),
			units.HumanSize(float64(len(part.Content))),
		)
		attachmentsDetails = append(attachmentsDetails, line)
	}
	for _, e := range env.Errors {
		logger.Errorf("Envelope error: %s", e.Error())
	}

	if text == "" {
		text = envelope.Data.String()
	}

	formattedAttachmentsDetails := ""
	if len(attachmentsDetails) > 0 {
		formattedAttachmentsDetails = fmt.Sprintf(
			"Attachments:\n%s",
			strings.Join(attachmentsDetails, "\n"),
		)
	}

	from := envelope.MailFrom.String()
	to := JoinEmailAddresses(envelope.RcptTo)
	subject := env.GetHeader("subject")
	html := env.HTML

	fullMessageText, truncatedMessageText := FormatMessage(
		from,
		to,
		subject,
		text,
		formattedAttachmentsDetails,
		telegramConfig,
	)
	if truncatedMessageText == "" { // no need to truncate
		return &FormattedEmail{
			From:        from,
			To:          to,
			Subject:     subject,
			Text:        fullMessageText,
			HTML:        html,
			Attachments: attachments,
		}, nil
	}

	if len(fullMessageText) > telegramConfig.ForwardedAttachmentMaxSize {
		return nil, fmt.Errorf(
			"%w: length %d > max %d",
			errMessageTooLarge,
			len(fullMessageText),
			telegramConfig.ForwardedAttachmentMaxSize,
		)
	}
	at := &FormattedAttachment{
		Filename: "full_message.txt",
		Caption:  "Full message",
		Content:  []byte(fullMessageText),
		FileType: AttachmentTypeDocument,
	}
	allAttachments := slices.Concat([]*FormattedAttachment{at}, attachments)
	return &FormattedEmail{
		From:        from,
		To:          to,
		Subject:     subject,
		Text:        truncatedMessageText,
		HTML:        html,
		Attachments: allAttachments,
	}, nil
}

func FormatMessage(
	from, to, subject, text string,
	formattedAttachmentsDetails string,
	telegramConfig *TelegramConfig,
) (fullMessageText, truncatedMessageText string) {
	fullMessageText = strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			"{body}", strings.TrimSpace(text),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(telegramConfig.MessageTemplate),
	)
	fullMessageRunes := []rune(fullMessageText)
	if uint(len(fullMessageRunes)) <= telegramConfig.MessageLengthToSendAsFile {
		// No need to truncate
		return fullMessageText, ""
	}

	emptyMessageText := strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			"{body}", strings.TrimSpace(fmt.Sprintf(".%s", BodyTruncated)),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(telegramConfig.MessageTemplate),
	)
	emptyMessageRunes := []rune(emptyMessageText)
	if uint(len(emptyMessageRunes)) >= telegramConfig.MessageLengthToSendAsFile {
		// Impossible to truncate properly
		return fullMessageText, string(fullMessageRunes[:telegramConfig.MessageLengthToSendAsFile])
	}

	maxBodyLength := telegramConfig.MessageLengthToSendAsFile - uint(len(emptyMessageRunes))
	truncatedMessageText = strings.TrimSpace(
		strings.NewReplacer(
			"\\n", "\n",
			"{from}", from,
			"{to}", to,
			"{subject}", subject,
			// TODO cut by paragraphs + respect formatting
			"{body}", strings.TrimSpace(fmt.Sprintf("%s%s",
				string([]rune(strings.TrimSpace(text))[:maxBodyLength]), BodyTruncated)),
			"{attachments_details}", formattedAttachmentsDetails,
		).Replace(telegramConfig.MessageTemplate),
	)
	if uint(len([]rune(truncatedMessageText))) > telegramConfig.MessageLengthToSendAsFile {
		panic(fmt.Errorf("%w: maxBodyLength=%d, text=%s", errUnexpectedTruncation, maxBodyLength, truncatedMessageText))
	}
	return fullMessageText, truncatedMessageText
}

func GuessContentType(contentType, filename string) string {
	if contentType != "application/octet-stream" {
		return contentType
	}
	guessedType := mime.TypeByExtension(filepath.Ext(filename))
	if guessedType != "" {
		return guessedType
	}
	return contentType // Give up
}

func FileIsImage(contentType string) bool {
	switch contentType {
	case
		// "image/gif",  // sent as a static image
		// "image/x-ms-bmp",  // rendered as document
		"image/jpeg",
		"image/png":
		return true
	}
	return false
}

func JoinEmailAddresses(a []mail.Address) string {
	s := make([]string, 0, len(a))
	for i := range a {
		s = append(s, a[i].String())
	}
	return strings.Join(s, ", ")
}

func EscapeMultiLine(b []byte) string {
	// Apparently errors returned by smtp must not contain newlines,
	// otherwise the data after the first newline is not getting
	// to the parsed message.
	s := string(b)
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return s
}

func SanitizeBotToken(s, botToken string) string {
	return strings.ReplaceAll(s, botToken, "***")
}


func awaitShutdown(ctx context.Context, d *guerrilla.Daemon) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)
	defer stop()

	<-ctx.Done()
	logger.Info("Shutdown signal caught")

	// Graceful shutdown with timeout
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
