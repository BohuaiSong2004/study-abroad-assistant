// Package mail sends transactional email (recommendation invitations,
// password resets) over SMTP, or logs it in development.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/BohuaiSong2004/study-abroad-assistant/internal/platform"
)

// Message is a plain-text email.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender delivers messages.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// FromEnv builds the sender from MAIL_MODE (log|smtp), SMTP_ADDR,
// SMTP_USERNAME, SMTP_PASSWORD and MAIL_FROM. The log sender writes message
// bodies (which contain secret links) to the log, so it is refused when
// ENVIRONMENT=production.
func FromEnv(log *slog.Logger) (Sender, error) {
	from := platform.Env("MAIL_FROM", "Waypoint <no-reply@waypoint.local>")
	if _, err := mail.ParseAddress(from); err != nil {
		return nil, fmt.Errorf("MAIL_FROM: %w", err)
	}
	switch mode := platform.Env("MAIL_MODE", "log"); mode {
	case "log":
		if platform.Env("ENVIRONMENT", "") == "production" {
			return nil, errors.New("MAIL_MODE=log logs secret links; use smtp in production")
		}
		return LogSender{Log: log}, nil
	case "smtp":
		addr := platform.Env("SMTP_ADDR", "")
		if addr == "" {
			return nil, errors.New("MAIL_MODE=smtp requires SMTP_ADDR")
		}
		return &SMTPSender{Addr: addr, From: from, Username: platform.Env("SMTP_USERNAME", ""), Password: platform.Env("SMTP_PASSWORD", "")}, nil
	default:
		return nil, fmt.Errorf("unknown MAIL_MODE %q (want log or smtp)", mode)
	}
}

// LogSender writes messages to the log (development).
type LogSender struct{ Log *slog.Logger }

// Send logs m.
func (s LogSender) Send(ctx context.Context, m Message) error {
	s.Log.InfoContext(ctx, "email (MAIL_MODE=log, not sent)", "to", m.To, "subject", m.Subject, "body", m.Text)
	return nil
}

// SMTPSender delivers through an SMTP relay (Mailpit in development, SES or
// another provider's SMTP endpoint in production). STARTTLS is used when
// the server offers it; credentials are only sent over TLS or to localhost
// (net/smtp enforces that).
type SMTPSender struct {
	Addr     string // host:port
	From     string // RFC 5322 address, e.g. "Waypoint <no-reply@example.com>"
	Username string
	Password string
}

// Send delivers m, giving up when ctx expires.
func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	msg, err := Build(from, to, m, time.Now())
	if err != nil {
		return err
	}
	return s.deliver(ctx, from.Address, to.Address, msg)
}

func (s *SMTPSender) deliver(ctx context.Context, from, to string, msg []byte) error {
	host, _, err := net.SplitHostPort(s.Addr)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.Addr)
	if err != nil {
		return err
	}
	// One deadline for the whole exchange: a relay that stops answering
	// cannot hold the request (or a goroutine) forever.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if s.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// Build renders the RFC 5322 message. Header values are stripped of line
// breaks (subjects include user-supplied names; a CR/LF would inject
// headers) and encoded per RFC 2047; the body is quoted-printable UTF-8.
func Build(from, to *mail.Address, m Message, now time.Time) ([]byte, error) {
	var b bytes.Buffer
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := "waypoint.local"
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 {
		domain = from.Address[i+1:]
	}
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject)))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="utf-8"`)
	header("Content-Transfer-Encoding", "quoted-printable")
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	if _, err := w.Write([]byte(strings.ReplaceAll(m.Text, "\n", "\r\n"))); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r == '\r' || r == '\n' }), " ")
}

// Capture records messages instead of sending them (tests).
type Capture struct {
	ch  chan Message
	Err error // returned by Send when set
}

// NewCapture creates a capture with room for n messages.
func NewCapture(n int) *Capture { return &Capture{ch: make(chan Message, n)} }

// Send records m.
func (c *Capture) Send(_ context.Context, m Message) error {
	if c.Err != nil {
		return c.Err
	}
	c.ch <- m
	return nil
}

// Next waits up to d for the next message.
func (c *Capture) Next(d time.Duration) (Message, bool) {
	select {
	case m := <-c.ch:
		return m, true
	case <-time.After(d):
		return Message{}, false
	}
}
