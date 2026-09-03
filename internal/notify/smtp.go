package notify

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// TLSMode is how the connection to the relay is protected.
type TLSMode string

const (
	// TLSStartTLS connects in the clear and upgrades. The usual choice on 587.
	TLSStartTLS TLSMode = "starttls"
	// TLSImplicit wraps the connection from the first byte. Port 465.
	TLSImplicit TLSMode = "implicit"
	// TLSNone is plaintext, and is refused unless the relay is on loopback.
	// A local mail catcher during development is the only legitimate use.
	TLSNone TLSMode = "none"
)

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string

	// From is the envelope sender and the From header. A single address for
	// the whole service rather than one per tenant: sending as a customer's
	// own domain requires their SPF and DKIM records, and forging it without
	// them is how a service ends up on a blocklist.
	From    Recipient
	TLS     TLSMode
	Timeout time.Duration
}

// SMTPSender delivers over SMTP.
type SMTPSender struct {
	cfg  SMTPConfig
	addr string
}

func NewSMTPSender(cfg SMTPConfig) (*SMTPSender, error) {
	if cfg.Host == "" {
		return nil, errors.New("notify: smtp host is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLS == "" {
		cfg.TLS = TLSStartTLS
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if !cfg.From.Valid() {
		return nil, fmt.Errorf("notify: %q is not a usable From address", cfg.From.Email)
	}

	// Plaintext is refused off the loopback interface, and refused outright if
	// credentials are involved. SMTP AUTH over an unencrypted connection sends
	// the password base64-encoded, which is not encoding anything - it is the
	// password, in the clear, to anyone on the path.
	if cfg.TLS == TLSNone {
		if cfg.Username != "" || cfg.Password != "" {
			return nil, errors.New("notify: refusing to send SMTP credentials over an unencrypted connection")
		}
		if !isLoopback(cfg.Host) {
			return nil, fmt.Errorf("notify: refusing plaintext SMTP to %q; only a loopback relay may skip TLS", cfg.Host)
		}
	}

	return &SMTPSender{cfg: cfg, addr: net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))}, nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// A container hostname on a private network. Resolved rather than assumed,
	// because "mailpit" is loopback-equivalent in compose and "smtp.gmail.com"
	// is not.
	addrs, err := net.LookupIP(host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, ip := range addrs {
		if !ip.IsLoopback() && !ip.IsPrivate() {
			return false
		}
	}
	return true
}

func (s *SMTPSender) Send(ctx context.Context, m Message) error {
	to := deliverable(m.To)
	if len(to) == 0 {
		return ErrNoRecipients
	}

	body, err := s.build(m, to)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	conn, err := s.dial(ctx, deadline)
	if err != nil {
		return err
	}
	defer conn.Close()

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp handshake: %w", err)
	}
	// Quit sends the closing command; Close is the fallback if it fails, so a
	// connection is never left open on the relay.
	defer func() { _ = client.Quit() }()

	if s.cfg.TLS == TLSStartTLS {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			// Configured for STARTTLS and the relay does not offer it. Sending
			// anyway would be a silent downgrade, which is what an attacker
			// stripping the extension is counting on.
			return errors.New("smtp: relay does not offer STARTTLS but the configuration requires it")
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("smtp starttls: %w", err)
		}
	}

	if s.cfg.Username != "" {
		auth := smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := client.Mail(s.cfg.From.Email); err != nil {
		return fmt.Errorf("smtp from: %w", err)
	}
	for _, r := range to {
		if err := client.Rcpt(r.Email); err != nil {
			// One bad recipient must not lose the message for everybody else.
			// The relay's rejection is worth a log line, not a failure.
			continue
		}
	}

	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close: %w", err)
	}
	return nil
}

func (s *SMTPSender) dial(ctx context.Context, deadline time.Time) (net.Conn, error) {
	dialer := &net.Dialer{Deadline: deadline}

	if s.cfg.TLS == TLSImplicit {
		conn, err := tls.DialWithDialer(dialer, "tcp", s.addr,
			&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12})
		if err != nil {
			return nil, fmt.Errorf("smtp dial: %w", err)
		}
		return conn, nil
	}

	conn, err := dialer.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("smtp dial: %w", err)
	}
	_ = conn.SetDeadline(deadline)
	return conn, nil
}

// build assembles a multipart/alternative message.
//
// Written out rather than taken from a library because the parts that matter
// are the ones a library would hide: header folding, encoding of non-ASCII
// subjects, and the fact that every header value here derives from tenant data
// and must not be able to inject a header of its own.
func (s *SMTPSender) build(m Message, to []Recipient) ([]byte, error) {
	boundary, err := randomBoundary()
	if err != nil {
		return nil, err
	}

	addresses := make([]string, len(to))
	for i, r := range to {
		addresses[i] = r.String()
	}

	var b strings.Builder

	// mime.QEncoding handles a non-ASCII subject and, incidentally, makes
	// header injection impossible: a CR or LF in the input is encoded rather
	// than emitted. The merchant name reaches this from the database.
	b.WriteString("From: " + s.cfg.From.String() + "\r\n")
	b.WriteString("To: " + strings.Join(addresses, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", sanitizeHeader(m.Subject)) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")

	// Tells a well-behaved mail client this is machine-generated, which keeps
	// an out-of-office reply from bouncing back into the relay.
	b.WriteString("Auto-Submitted: auto-generated\r\n")
	b.WriteString("X-Auto-Response-Suppress: All\r\n")

	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")

	// Plain text first. multipart/alternative is ordered least to most
	// preferred, so the HTML part has to come second or clients show the text.
	writePart(&b, boundary, "text/plain; charset=utf-8", m.Text)
	writePart(&b, boundary, "text/html; charset=utf-8", m.HTML)

	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String()), nil
}

func writePart(b *strings.Builder, boundary, contentType, body string) {
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: " + contentType + "\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")

	// Quoted-printable, not raw. SMTP has a 998-octet line limit and the HTML
	// bodies exceed it; a relay that wraps a long line itself corrupts the
	// markup at whatever character it lands on.
	qp := quotedprintable.NewWriter(b)
	_, _ = io.WriteString(qp, body)
	_ = qp.Close()

	b.WriteString("\r\n")
}

// sanitizeHeader removes anything that could terminate a header line. Belt and
// braces with the Q-encoding above, which already handles it - but this is the
// kind of thing that must stay true if the encoding is ever changed.
func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

func randomBoundary() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate mime boundary: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
