package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// Channel config shapes (the sealed JSON, notifications.md §2). One struct per
// channel; send unmarshals the one matching the notifier's channel.
type emailConfig struct {
	SMTPHost string `json:"smtp_host"`
	SMTPPort int    `json:"smtp_port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"` // comma-separated recipients
}

type webhookConfig struct {
	WebhookURL string `json:"webhook_url"`
}

type telegramConfig struct {
	BotToken string `json:"bot_token"`
	ChatID   string `json:"chat_id"`
}

// send routes a rendered event to the channel's transport. cfg is the unsealed
// config JSON.
func (m *Manager) send(ctx context.Context, channel string, cfg []byte, ev domain.NotifyEvent) error {
	switch channel {
	case domain.NotifyChannelEmail:
		return m.sendEmail(cfg, ev)
	case domain.NotifyChannelDiscord:
		var c webhookConfig
		if err := json.Unmarshal(cfg, &c); err != nil {
			return fmt.Errorf("decoding discord config: %w", err)
		}
		return m.postJSON(ctx, c.WebhookURL, map[string]string{"content": ev.Title + "\n" + ev.Body})
	case domain.NotifyChannelSlack:
		var c webhookConfig
		if err := json.Unmarshal(cfg, &c); err != nil {
			return fmt.Errorf("decoding slack config: %w", err)
		}
		return m.postJSON(ctx, c.WebhookURL, map[string]string{"text": ev.Title + "\n" + ev.Body})
	case domain.NotifyChannelTelegram:
		var c telegramConfig
		if err := json.Unmarshal(cfg, &c); err != nil {
			return fmt.Errorf("decoding telegram config: %w", err)
		}
		url := fmt.Sprintf("%s/bot%s/sendMessage", telegramAPI, c.BotToken)
		return m.postJSON(ctx, url, map[string]string{"chat_id": c.ChatID, "text": ev.Title + "\n" + ev.Body})
	default:
		return fmt.Errorf("unknown channel %q", channel)
	}
}

// postJSON POSTs body as JSON and treats a non-2xx as a delivery failure.
func (m *Manager) postJSON(ctx context.Context, url string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("building request: %w", redactURL(err))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("posting: %w", redactURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain a little of the body so the connection can be reused; ignore it —
	// send-and-forget, we never follow the response anywhere (spec §6).
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("endpoint returned %s", resp.Status)
	}
	return nil
}

// redactURL strips the request URL out of a transport error before it is
// returned (and later logged by fanOut / the test handler). The URL can be the
// secret itself — a Discord/Slack webhook is a bearer capability and the
// Telegram bot token sits in the path (notifications.md §6, ENGINEERING rule
// 20). A *url.Error's message embeds that URL, so unwrap it to its cause, which
// carries the failure reason (dial/timeout) without the URL.
func redactURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// sendEmail delivers via SMTP (stdlib net/smtp). smtp.SendMail issues STARTTLS
// when the server advertises it; auth is used only when a username is set.
func (m *Manager) sendEmail(cfg []byte, ev domain.NotifyEvent) error {
	var c emailConfig
	if err := json.Unmarshal(cfg, &c); err != nil {
		return fmt.Errorf("decoding email config: %w", err)
	}
	to := splitRecipients(c.To)
	if len(to) == 0 {
		return fmt.Errorf("no recipients")
	}
	msg := buildMessage(c.From, to, ev.Title, ev.Title+"\n\n"+ev.Body)
	addr := fmt.Sprintf("%s:%d", c.SMTPHost, c.SMTPPort)
	var auth smtp.Auth
	if c.Username != "" {
		auth = smtp.PlainAuth("", c.Username, c.Password, c.SMTPHost)
	}
	if err := smtp.SendMail(addr, auth, c.From, to, msg); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

// sanitizeHeader neutralises CR and LF in a header value so it cannot inject
// additional headers or split the message (email header injection).
func sanitizeHeader(v string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
}

// splitRecipients parses a comma-separated recipient list, trimming blanks.
func splitRecipients(list string) []string {
	var out []string
	for _, r := range strings.Split(list, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

// buildMessage assembles a minimal RFC 5322 plain-text message.
func buildMessage(from string, to []string, subject, body string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	// Neutralise CR/LF in the subject so an app/database name propagated into
	// ev.Title (NotifyDeploy/NotifyBackup) cannot inject extra SMTP headers or
	// split the message.
	fmt.Fprintf(&b, "Subject: %s\r\n", sanitizeHeader(subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}
