package accountops

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type SMTPConfig struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Password           string `json:"password,omitempty"`
	PasswordConfigured bool   `json:"password_configured"`
	From               string `json:"from"`
	TLSMode            string `json:"tls_mode"`
}

func (c SMTPConfig) Validate() error {
	if c.Host == "" && c.From == "" && c.Username == "" && c.Password == "" {
		return nil
	}
	if strings.TrimSpace(c.Host) == "" || strings.ContainsAny(c.Host, "\r\n/ ") || c.Port < 1 || c.Port > 65535 {
		return errors.New("invalid SMTP host or port")
	}
	a, e := mail.ParseAddress(c.From)
	if e != nil || a.Address != c.From || strings.ContainsAny(c.From, "\r\n") {
		return errors.New("invalid sender address")
	}
	if c.TLSMode != "starttls" && c.TLSMode != "tls" {
		return errors.New("SMTP requires STARTTLS or TLS")
	}
	return nil
}
func SMTPMessage(from, to, subject, body string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(body))
	var lines strings.Builder
	for len(encoded) > 76 {
		lines.WriteString(encoded[:76])
		lines.WriteString("\r\n")
		encoded = encoded[76:]
	}
	lines.WriteString(encoded)
	lines.WriteString("\r\n")
	return "From: " + from + "\r\nTo: " + to + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", subject) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + lines.String()
}

type SMTPSender struct {
	Config func(context.Context) (SMTPConfig, error)
}

func (s SMTPSender) SendEmail(ctx context.Context, to, subject, body string) error {
	c, e := s.Config(ctx)
	if e != nil {
		return e
	}
	if e = c.Validate(); e != nil {
		return e
	}
	if c.Host == "" {
		return errors.New("SMTP not configured")
	}
	a, e := mail.ParseAddress(to)
	if e != nil || a.Address != to || strings.ContainsAny(to, "\r\n") {
		return errors.New("invalid recipient")
	}
	address := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, e := d.DialContext(ctx, "tcp", address)
	if e != nil {
		return e
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if t, ok := ctx.Deadline(); ok && t.Before(deadline) {
		deadline = t
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	tc := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}
	if c.TLSMode == "tls" {
		secure := tls.Client(conn, tc)
		if e = secure.HandshakeContext(ctx); e != nil {
			return e
		}
		conn = secure
	}
	client, e := smtp.NewClient(conn, c.Host)
	if e != nil {
		return e
	}
	defer client.Close()
	if c.TLSMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return fmt.Errorf("SMTP STARTTLS unavailable")
		}
		if e = client.StartTLS(tc); e != nil {
			return e
		}
	}
	if c.Username != "" {
		if e = client.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); e != nil {
			return e
		}
	}
	if e = client.Mail(c.From); e != nil {
		return e
	}
	if e = client.Rcpt(to); e != nil {
		return e
	}
	w, e := client.Data()
	if e != nil {
		return e
	}
	if _, e = w.Write([]byte(SMTPMessage(c.From, to, subject, body))); e != nil {
		return e
	}
	if e = w.Close(); e != nil {
		return e
	}
	return client.Quit()
}
