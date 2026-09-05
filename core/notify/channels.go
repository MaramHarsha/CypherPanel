package notify

import (
	"bytes"
	"context"
	"crypto/tls"
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

// MailConfig is everything needed to hand a message to an SMTP server. It is
// exported because the panel now has mail of its own to send — account mail,
// which belongs to no project (docs/features/panel-mail.md) — and that must go
// through this sender rather than a second copy of it.
// Transport security for an SMTP conversation. The zero value is STARTTLS
// because that is what the panel did before the mode was configurable, and a
// silent downgrade on upgrade would be the worst possible default.
const (
	// TLSStartTLS upgrades a plaintext connection with STARTTLS and refuses to
	// send if the server will not offer it. Port 587.
	TLSStartTLS = "starttls"
	// TLSImplicit wraps the connection in TLS from the first byte. Port 465.
	TLSImplicit = "implicit"
	// TLSNone sends in the clear. Only defensible for a relay on localhost or
	// a private network the operator controls.
	TLSNone = "none"
)

// ValidMailTLS reports whether v names a transport-security mode. The empty
// string is valid and means STARTTLS.
func ValidMailTLS(v string) bool {
	switch v {
	case "", TLSStartTLS, TLSImplicit, TLSNone:
		return true
	}
	return false
}

type MailConfig struct {
	SMTPHost string
	SMTPPort int
	Username string
	From     string
	Password string
	// TLS is one of the constants above; empty means TLSStartTLS.
	TLS string
}

// SendMail delivers one message over SMTP.
//
// The three modes are driven explicitly rather than inferred from the port,
// because inference is what makes a misconfigured panel fail at the moment it
// matters. STARTTLS is *required* in that mode: stdlib smtp.SendMail upgrades
// only when the server advertises STARTTLS and otherwise sends the credential
// in the clear, which is a downgrade an operator who chose STARTTLS did not
// agree to. Auth is used only when a username is set.
func SendMail(c MailConfig, to []string, subject, body string) error {
	if len(to) == 0 {
		return fmt.Errorf("no recipients")
	}
	if !ValidMailTLS(c.TLS) {
		return fmt.Errorf("unknown TLS mode %q", c.TLS)
	}
	msg := buildMessage(c.From, to, subject, body)
	addr := fmt.Sprintf("%s:%d", c.SMTPHost, c.SMTPPort)

	client, err := dialSMTP(c, addr)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	if c.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.SMTPHost)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := client.Mail(c.From); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	for _, rcpt := range to {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp recipient: %w", err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := wc.Write(msg); err != nil {
		_ = wc.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

// dialSMTP opens the connection in the requested transport security mode and
// returns a client that has already greeted the server.
func dialSMTP(c MailConfig, addr string) (*smtp.Client, error) {
	if c.TLS == TLSImplicit {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, fmt.Errorf("smtp dial (implicit TLS): %w", err)
		}
		client, err := smtp.NewClient(conn, c.SMTPHost)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("smtp greet: %w", err)
		}
		return client, nil
	}

	client, err := smtp.Dial(addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial: %w", err)
	}
	if c.TLS == TLSNone {
		return client, nil
	}
	// STARTTLS, and it is not optional: a server that will not upgrade is a
	// server this configuration must not talk to.
	if ok, _ := client.Extension("STARTTLS"); !ok {
		_ = client.Close()
		return nil, fmt.Errorf("smtp: the server does not offer STARTTLS — choose implicit TLS (port 465) or none if this relay is trusted")
	}
	if err := client.StartTLS(&tls.Config{ServerName: c.SMTPHost, MinVersion: tls.VersionTLS12}); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("smtp starttls: %w", err)
	}
	return client, nil
}

// sendEmail delivers a notifier's event through the same sender.
func (m *Manager) sendEmail(cfg []byte, ev domain.NotifyEvent) error {
	var c emailConfig
	if err := json.Unmarshal(cfg, &c); err != nil {
		return fmt.Errorf("decoding email config: %w", err)
	}
	return SendMail(
		MailConfig{SMTPHost: c.SMTPHost, SMTPPort: c.SMTPPort, Username: c.Username, From: c.From, Password: c.Password},
		splitRecipients(c.To), ev.Title, ev.Title+"\n\n"+ev.Body,
	)
}

// SanitizeHeader neutralises CR and LF in a header value so it cannot inject
// additional headers or split the message (email header injection). Exported so
// callers assembling a message body from anything a person typed can apply the
// same rule to it.
func SanitizeHeader(v string) string {
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
	// Every header value is neutralised, not just the subject: a CR or LF in
	// any of them ends the header and starts another, which is how a single
	// recipient field becomes a Bcc to somewhere else. From and To carry
	// operator- and user-supplied values just as Subject does.
	fmt.Fprintf(&b, "From: %s\r\n", SanitizeHeader(from))
	recipients := make([]string, 0, len(to))
	for _, addr := range to {
		recipients = append(recipients, SanitizeHeader(addr))
	}
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(recipients, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", SanitizeHeader(subject))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(normalizeBody(body))
	return []byte(b.String())
}

// normalizeBody makes an arbitrary string safe to carry as the message body.
//
// The body sits after the blank line, so it cannot introduce a header — that
// class is closed by SanitizeHeader above. What is left is line-ending
// hygiene, and doing it by hand matters for one case the obvious
// ReplaceAll("\n", "\r\n") gets wrong: a lone CR. SMTP lines are CRLF, a bare
// CR inside DATA is a protocol violation, and a body assembled from a commit
// message, a container log line or an operator's own note can contain one.
// Every line ending — CRLF, lone CR, lone LF — collapses to exactly one CRLF,
// so what the recipient sees is what the panel meant to say.
//
// Dot-stuffing is deliberately NOT done here: net/smtp writes the body through
// textproto's DotWriter, which escapes a leading "." itself. Doing it twice
// would put the dot back in the delivered text.
func normalizeBody(body string) string {
	var b strings.Builder
	b.Grow(len(body) + len(body)/16)
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\r':
			b.WriteString("\r\n")
			// A CRLF pair is one ending, not two.
			if i+1 < len(body) && body[i+1] == '\n' {
				i++
			}
		case '\n':
			b.WriteString("\r\n")
		default:
			b.WriteByte(body[i])
		}
	}
	return b.String()
}
