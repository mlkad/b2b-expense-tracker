package notify

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
)

func TestSMTPSenderRefusesUnsafeConfiguration(t *testing.T) {
	from := Recipient{Name: "Expense Tracker", Email: "noreply@example.com"}

	t.Run("no host", func(t *testing.T) {
		if _, err := NewSMTPSender(SMTPConfig{From: from}); err == nil {
			t.Fatal("accepted a sender with no host")
		}
	})

	t.Run("unusable from address", func(t *testing.T) {
		for _, addr := range []string{"", "not an address", "@example.com"} {
			if _, err := NewSMTPSender(SMTPConfig{Host: "smtp.example.com", From: Recipient{Email: addr}}); err == nil {
				t.Errorf("accepted %q as the From address", addr)
			}
		}
	})

	// SMTP AUTH over an unencrypted connection sends the password
	// base64-encoded, which is not encoding anything - it is the password, in
	// the clear, to anyone on the path.
	t.Run("credentials over plaintext are refused outright", func(t *testing.T) {
		_, err := NewSMTPSender(SMTPConfig{
			Host: "127.0.0.1", Port: 1025, TLS: TLSNone,
			Username: "user", Password: "hunter2", From: from,
		})
		if err == nil {
			t.Fatal("accepted credentials over an unencrypted connection")
		}
		if !strings.Contains(err.Error(), "credentials") {
			t.Errorf("the error does not explain why: %v", err)
		}
	})

	t.Run("plaintext to a remote relay is refused", func(t *testing.T) {
		_, err := NewSMTPSender(SMTPConfig{
			Host: "smtp.sendgrid.net", Port: 25, TLS: TLSNone, From: from,
		})
		if err == nil {
			t.Fatal("accepted plaintext SMTP to a remote host")
		}
	})

	// A local mail catcher during development is the one legitimate use.
	t.Run("plaintext to loopback is allowed", func(t *testing.T) {
		for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
			if _, err := NewSMTPSender(SMTPConfig{Host: host, Port: 1025, TLS: TLSNone, From: from}); err != nil {
				t.Errorf("refused a loopback relay at %q: %v", host, err)
			}
		}
	})

	t.Run("defaults are filled in", func(t *testing.T) {
		s, err := NewSMTPSender(SMTPConfig{Host: "smtp.example.com", From: from})
		if err != nil {
			t.Fatal(err)
		}
		if s.cfg.Port != 587 || s.cfg.TLS != TLSStartTLS || s.cfg.Timeout == 0 {
			t.Fatalf("defaults not applied: %+v", s.cfg)
		}
	})
}

// Every header value here derives from tenant data, so a CR or LF in a subject
// must not be able to add a header of its own.
func TestBuildRefusesHeaderInjection(t *testing.T) {
	s, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: 1025, TLS: TLSNone,
		From: Recipient{Name: "Expense Tracker", Email: "noreply@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	hostile := Message{
		Subject: "Claim approved\r\nBcc: attacker@evil.test\r\nX-Injected: yes",
		Text:    "body",
		HTML:    "<p>body</p>",
	}
	raw, err := s.build(hostile, []Recipient{{Email: "ada@acme.test"}})
	if err != nil {
		t.Fatal(err)
	}

	// Parsed by net/mail rather than searched as a string. The question is not
	// whether the text "Bcc:" appears - it does, harmlessly, inside the
	// subject value, because the line breaks were replaced with spaces - but
	// whether a new header line was created. An independent parser is the
	// thing that answers that.
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("the message does not parse: %v", err)
	}

	for _, injected := range []string{"Bcc", "X-Injected", "Cc"} {
		if got := parsed.Header.Get(injected); got != "" {
			t.Fatalf("a %s header was injected through the subject: %q", injected, got)
		}
	}

	// The subject still carries the text, on one line, so nothing is silently
	// dropped either.
	subject := parsed.Header.Get("Subject")
	if !strings.Contains(subject, "Claim approved") {
		t.Errorf("the subject lost its content: %q", subject)
	}
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("the subject still contains a line break: %q", subject)
	}
}

func TestBuildProducesAWellFormedMultipart(t *testing.T) {
	s, err := NewSMTPSender(SMTPConfig{
		Host: "127.0.0.1", Port: 1025, TLS: TLSNone,
		From: Recipient{Name: "Expense Tracker", Email: "noreply@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := s.build(Message{
		Subject: "Claim approved — Café Ünïcödé",
		Text:    "plain body",
		HTML:    "<p>html body</p>",
	}, []Recipient{{Email: "ada@acme.test", Name: "Ada"}, {Email: "grace@acme.test"}})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	for _, want := range []string{
		"From: \"Expense Tracker\" <noreply@example.com>",
		"MIME-Version: 1.0",
		"multipart/alternative",
		"text/plain; charset=utf-8",
		"text/html; charset=utf-8",
		// Machine-generated, so an out-of-office reply does not bounce back
		// into the relay.
		"Auto-Submitted: auto-generated",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the message is missing %q", want)
		}
	}

	// Both recipients on one To line.
	if !strings.Contains(body, "ada@acme.test") || !strings.Contains(body, "grace@acme.test") {
		t.Error("a recipient is missing from the To header")
	}

	// A non-ASCII subject must be encoded, not sent as raw UTF-8 bytes in a
	// header - several relays reject that outright.
	if strings.Contains(body, "Café Ünïcödé") {
		t.Error("the subject was not Q-encoded")
	}
	if !strings.Contains(body, "=?utf-8?q?") && !strings.Contains(body, "=?UTF-8?q?") {
		t.Errorf("no encoded-word in the subject line")
	}

	// The text part must come first: multipart/alternative is ordered least to
	// most preferred, so HTML second is what makes a capable client show it.
	textAt := strings.Index(body, "text/plain")
	htmlAt := strings.Index(body, "text/html")
	if textAt < 0 || htmlAt < 0 || textAt > htmlAt {
		t.Error("the parts are in the wrong order; clients would show the wrong one")
	}

	// Quoted-printable, because SMTP has a 998-octet line limit and a relay
	// that wraps a long HTML line itself corrupts the markup.
	if !strings.Contains(body, "Content-Transfer-Encoding: quoted-printable") {
		t.Error("bodies are not quoted-printable encoded")
	}
}
