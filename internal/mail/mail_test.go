package mail

import (
	"bufio"
	"context"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"
)

// fakeSMTP accepts one message and returns what was received.
func fakeSMTP(t *testing.T) (string, <-chan [3]string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan [3]string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		say := func(s string) { _, _ = io.WriteString(c, s+"\r\n") }
		say("220 fake ESMTP")
		var from, rcpt string
		var data strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				from = strings.TrimSpace(line[10:])
				say("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				rcpt = strings.TrimSpace(line[8:])
				say("250 ok")
			case cmd == "DATA":
				say("354 go ahead")
				for {
					l, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if l == ".\r\n" {
						break
					}
					data.WriteString(l)
				}
				say("250 queued")
				got <- [3]string{from, rcpt, data.String()}
			case cmd == "QUIT":
				say("221 bye")
				return
			default:
				say("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSMTPSenderDeliversAPlainTextUTF8Message(t *testing.T) {
	addr, got := fakeSMTP(t)
	s := &SMTPSender{Addr: addr, From: "Waypoint <no-reply@waypoint.test>"}
	body := "Bonjour Zoë,\n\nOpen: https://waypoint.test/recommend.html#token=abc\n"
	err := s.Send(context.Background(), Message{To: "prof@uni.test", Subject: "Letter for Zoë\r\nBcc: victim@evil.test", Text: body})
	if err != nil {
		t.Fatal(err)
	}
	var r [3]string
	select {
	case r = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
	if r[0] != "<no-reply@waypoint.test>" || r[1] != "<prof@uni.test>" {
		t.Fatalf("envelope %q %q", r[0], r[1])
	}
	msg, err := mail.ReadMessage(strings.NewReader(r[2]))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Bcc") != "" {
		t.Fatal("header injection through the subject")
	}
	subj, _ := new(mimeDecoder).decode(msg.Header.Get("Subject"))
	if subj != "Letter for Zoë Bcc: victim@evil.test" {
		t.Fatalf("subject %q", subj)
	}
	text, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if strings.ReplaceAll(string(text), "\r\n", "\n") != body {
		t.Fatalf("body %q", text)
	}
	if msg.Header.Get("Message-Id") == "" || !strings.Contains(msg.Header.Get("Content-Type"), "utf-8") {
		t.Fatalf("headers %v", msg.Header)
	}
}

func TestSMTPSenderHonoursTheContext(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // accepts, never answers
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = (&SMTPSender{Addr: ln.Addr().String(), From: "a@b.test"}).Send(ctx, Message{To: "c@d.test", Subject: "s", Text: "t"})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("MAIL_MODE", "smtp")
	if _, err := FromEnv(nil); err == nil {
		t.Fatal("smtp without SMTP_ADDR accepted")
	}
	t.Setenv("SMTP_ADDR", "mailpit:1025")
	if s, err := FromEnv(nil); err != nil || s.(*SMTPSender).Addr != "mailpit:1025" {
		t.Fatal(s, err)
	}
	t.Setenv("MAIL_MODE", "log")
	t.Setenv("ENVIRONMENT", "production")
	if _, err := FromEnv(nil); err == nil {
		t.Fatal("log sender allowed in production")
	}
	t.Setenv("MAIL_MODE", "carrier-pigeon")
	t.Setenv("ENVIRONMENT", "")
	if _, err := FromEnv(nil); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

type mimeDecoder struct{}

func (mimeDecoder) decode(s string) (string, error) {
	return new(mime.WordDecoder).DecodeHeader(s)
}
