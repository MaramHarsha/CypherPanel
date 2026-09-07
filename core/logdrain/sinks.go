package logdrain

// The three transports (log-drains.md §3).
//
// Every one of them goes out through the EGRESS GUARD, so a drain cannot be
// pointed at the plane's own loopback, at link-local metadata, or at anything
// else inside the operator's network that the panel can reach and the caller
// cannot (threat-model §5.11). A log drain is an operator-supplied URL that the
// control plane connects to on a schedule, which is exactly the SSRF shape that
// guard exists for.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/egress"
)

const shipTimeout = 30 * time.Second

// LokiConfig is a push endpoint plus whatever headers the deployment needs.
type LokiConfig struct {
	URL string `json:"url"`
	// Headers is where an X-Scope-OrgID or a bearer token goes. The WHOLE
	// config is sealed, so which of these is a secret does not have to be
	// decided here — and at one site the tenant id IS the access boundary.
	Headers map[string]string `json:"headers,omitempty"`
	// Labels are added to every stream this drain pushes, on top of the ones
	// the record already carries.
	Labels map[string]string `json:"labels,omitempty"`
}

// SyslogConfig is RFC 5424 over TCP.
type SyslogConfig struct {
	Address string `json:"address"`
	// AppName is the APP-NAME field. Per-line it is the application's own name;
	// this is the fallback for a line whose application could not be resolved.
	AppName string `json:"app_name,omitempty"`
}

// S3Config is a prefix under an existing Backup Target. There is no second
// credential here on purpose.
type S3Config struct {
	Prefix string `json:"prefix,omitempty"`
}

// ConfigHint masks a drain's config for the API, by unsealing in-process and
// discarding the plaintext — the same shape notifiers, panel mail and the DNS
// provider already use under that name.
func ConfigHint(kind string, cfg []byte) string {
	switch kind {
	case domain.DrainLoki:
		var c LokiConfig
		_ = json.Unmarshal(cfg, &c)
		return maskURL(c.URL)
	case domain.DrainSyslog:
		var c SyslogConfig
		_ = json.Unmarshal(cfg, &c)
		return c.Address
	case domain.DrainS3:
		var c S3Config
		_ = json.Unmarshal(cfg, &c)
		if c.Prefix == "" {
			return "the target's own prefix"
		}
		return c.Prefix
	}
	return kind
}

// maskURL keeps the host and hides the path: a Loki push URL can carry a tenant
// in its path at some deployments.
func maskURL(raw string) string {
	if raw == "" {
		return ""
	}
	i := strings.Index(raw, "://")
	if i < 0 {
		return "•••"
	}
	rest := raw[i+3:]
	host := rest
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		host = rest[:j]
	}
	return raw[:i+3] + host + "/•••"
}

func defaultSink(d domain.LogDrain, cfg []byte, target domain.BackupTarget) (Sink, error) {
	switch d.Kind {
	case domain.DrainLoki:
		var c LokiConfig
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, fmt.Errorf("logdrain: parsing the loki config: %w", err)
		}
		if c.URL == "" {
			return nil, fmt.Errorf("logdrain: the loki config has no url")
		}
		return &lokiSink{cfg: c, client: egress.HTTPClient(shipTimeout)}, nil
	case domain.DrainSyslog:
		var c SyslogConfig
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, fmt.Errorf("logdrain: parsing the syslog config: %w", err)
		}
		if c.Address == "" {
			return nil, fmt.Errorf("logdrain: the syslog config has no address")
		}
		return &syslogSink{cfg: c}, nil
	case domain.DrainS3:
		var c S3Config
		if err := json.Unmarshal(cfg, &c); err != nil {
			return nil, fmt.Errorf("logdrain: parsing the s3 config: %w", err)
		}
		if target.ID == "" {
			return nil, fmt.Errorf("logdrain: an s3 drain needs a backup target")
		}
		return &s3Sink{cfg: c, target: target, upload: s3Upload}, nil
	}
	return nil, fmt.Errorf("logdrain: unknown kind %q", d.Kind)
}

// ─── loki ───────────────────────────────────────────────────────────────────

type lokiSink struct {
	cfg    LokiConfig
	client *http.Client
}

// Ship pushes one stream per (project, environment, app) triple, which is what
// makes a sink's own filtering useful: a query for one environment is a label
// selector rather than a full-text scan.
func (s *lokiSink) Ship(ctx context.Context, records []Record) error {
	type stream struct {
		Stream map[string]string `json:"stream"`
		Values [][2]string       `json:"values"`
	}
	byKey := map[string]*stream{}
	order := []string{}
	for _, r := range records {
		key := r.ProjectID + "\x00" + r.Environment + "\x00" + r.AppID
		st := byKey[key]
		if st == nil {
			labels := map[string]string{
				"job": "cypherpanel", "app": r.App, "project": r.Project,
				"environment": r.Environment, "server": r.Server,
			}
			for k, v := range s.cfg.Labels {
				labels[k] = v
			}
			for k, v := range labels {
				if v == "" {
					delete(labels, k)
				}
			}
			st = &stream{Stream: labels}
			byKey[key] = st
			order = append(order, key)
		}
		st.Values = append(st.Values, [2]string{strconv.FormatInt(r.TS.UnixNano(), 10), r.Line})
	}
	payload := struct {
		Streams []*stream `json:"streams"`
	}{}
	for _, k := range order {
		payload.Streams = append(payload.Streams, byKey[k])
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status only. A sink's error page is not ours to store, and it can
		// echo back the headers we sent it (ENGINEERING rule 20).
		return fmt.Errorf("loki answered %d", resp.StatusCode)
	}
	return nil
}

func (s *lokiSink) Close() error { return nil }

// ─── syslog ─────────────────────────────────────────────────────────────────

type syslogSink struct {
	cfg  SyslogConfig
	conn net.Conn
}

// Ship writes RFC 5424 frames over a connection it keeps open between batches —
// reconnecting per batch would be a TCP handshake per five seconds forever.
func (s *syslogSink) Ship(ctx context.Context, records []Record) error {
	if s.conn == nil {
		conn, err := egress.Dial(s.cfg.Address, shipTimeout)
		if err != nil {
			return err
		}
		s.conn = conn
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = s.conn.SetWriteDeadline(deadline)
	} else {
		_ = s.conn.SetWriteDeadline(time.Now().Add(shipTimeout))
	}

	var b strings.Builder
	for _, r := range records {
		app := r.App
		if app == "" {
			app = s.cfg.AppName
		}
		if app == "" {
			app = "cypherpanel"
		}
		// <134> is local0.info. Octet counting, because a log line can contain
		// a newline once a sink stops normalising and a length-prefixed frame
		// cannot be split by one.
		msg := fmt.Sprintf("<134>1 %s %s %s - - [cypherpanel project=%q environment=%q] %s",
			r.TS.UTC().Format(time.RFC3339), hostOrDash(r.Server), app, r.Project, r.Environment, r.Line)
		b.WriteString(strconv.Itoa(len(msg)))
		b.WriteByte(' ')
		b.WriteString(msg)
	}
	if _, err := s.conn.Write([]byte(b.String())); err != nil {
		// A half-open connection stays broken forever unless it is dropped, and
		// the retry is what reconnects.
		_ = s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

func (s *syslogSink) Close() error {
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func hostOrDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ─── s3 ─────────────────────────────────────────────────────────────────────

// s3Sink batches to an object per flush. Deliberately the simplest of the three:
// an archive is read rarely and written constantly, so one gzip-able JSON-lines
// object per batch beats anything cleverer.
type s3Sink struct {
	cfg    S3Config
	target domain.BackupTarget
	// Uploader is set by the wiring; nil means the drain reports that S3
	// archiving is not available on this build rather than silently dropping.
	upload func(ctx context.Context, target domain.BackupTarget, key string, body []byte) error
}

func (s *s3Sink) Ship(ctx context.Context, records []Record) error {
	if s.upload == nil {
		return fmt.Errorf("s3 log archiving is not wired on this panel")
	}
	body, err := MarshalRecords(records)
	if err != nil {
		return err
	}
	prefix := strings.Trim(s.cfg.Prefix, "/")
	if prefix == "" {
		prefix = "logs"
	}
	now := time.Now().UTC()
	key := fmt.Sprintf("%s/%s/%s.jsonl", prefix, now.Format("2006/01/02"), now.Format("150405.000000000"))
	return s.upload(ctx, s.target, key, body)
}

func (s *s3Sink) Close() error { return nil }

// SetS3Uploader attaches the object writer used by S3 drains.
func SetS3Uploader(fn func(ctx context.Context, target domain.BackupTarget, key string, body []byte) error) {
	s3Upload = fn
}

var s3Upload func(ctx context.Context, target domain.BackupTarget, key string, body []byte) error
