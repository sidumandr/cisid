package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// SMTP connection security.
const (
	// TLSStartTLS upgrades a plain connection with STARTTLS and refuses a
	// server that does not offer it (port 587).
	TLSStartTLS = "starttls"
	// TLSImplicit speaks TLS from the start (port 465).
	TLSImplicit = "tls"
	// TLSNone sends in the clear; only for a relay on the same host or
	// network. Passwords are refused over it except to localhost.
	TLSNone = "none"
)

// MailConfig is how the server reaches its SMTP relay.
type MailConfig struct {
	Host     string
	Port     int
	Username string // empty: no authentication
	Password string
	From     string // the sender, e.g. "cisid <ci@example.com>"
	TLS      string // TLSStartTLS, TLSImplicit or TLSNone
}

// Mailer sends email through an SMTP relay.
type Mailer struct {
	cfg  MailConfig
	from *mail.Address
	// tlsConfig is for tests, which talk to a relay with its own
	// certificate.
	tlsConfig *tls.Config
}

// NewMailer returns a Mailer for cfg.
func NewMailer(cfg MailConfig) (*Mailer, error) {
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("parse sender %q: %w", cfg.From, err)
	}
	switch cfg.TLS {
	case TLSStartTLS, TLSImplicit, TLSNone:
	default:
		return nil, fmt.Errorf("unknown TLS mode %q", cfg.TLS)
	}
	if cfg.Host == "" || cfg.Port <= 0 {
		return nil, errors.New("the SMTP host and port are required")
	}
	return &Mailer{cfg: cfg, from: from}, nil
}

// Send emails body to the addresses in to. Failures the relay may get
// over (its 4xx replies and network errors) are temporary.
func (m *Mailer) Send(ctx context.Context, to []string, subject, body string) error {
	msg, err := m.compose(to, subject, body)
	if err != nil {
		return err
	}
	if err := m.send(ctx, to, msg); err != nil {
		var tp *textproto.Error
		if errors.Is(err, errNoStartTLS) || (errors.As(err, &tp) && tp.Code >= 500) {
			return fmt.Errorf("mail relay %s: %w", m.cfg.Host, err)
		}
		return temporary("mail relay %s: %v", m.cfg.Host, err)
	}
	return nil
}

// errNoStartTLS is returned when STARTTLS is required but not offered.
var errNoStartTLS = errors.New("the relay does not offer STARTTLS; set CISID_SMTP_TLS=tls for port 465, or none for a relay on a trusted network")

func (m *Mailer) tls() *tls.Config {
	if m.tlsConfig != nil {
		return m.tlsConfig
	}
	return &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}
}

func (m *Mailer) send(ctx context.Context, to []string, msg []byte) error {
	addr := net.JoinHostPort(m.cfg.Host, strconv.Itoa(m.cfg.Port))
	dialer := &net.Dialer{}
	var (
		conn net.Conn
		err  error
	)
	if m.cfg.TLS == TLSImplicit {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: m.tls()}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	// The SMTP client has no context; a deadline and closing on
	// cancellation stand in for it.
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("greeting: %w", err)
	}
	defer func() { _ = c.Close() }()

	if m.cfg.TLS == TLSStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errNoStartTLS
		}
		if err := c.StartTLS(m.tls()); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if m.cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("authenticate: %w", err)
		}
	}
	if err := c.Mail(m.from.Address); err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("recipient %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	return c.Quit()
}

// compose builds the message: plain text in UTF-8, quoted-printable, with
// the subject encoded so no name in it can add a header.
func (m *Mailer) compose(to []string, subject, body string) ([]byte, error) {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, fmt.Errorf("make message id: %w", err)
	}
	domain := m.from.Address[strings.LastIndex(m.from.Address, "@")+1:]

	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", m.from.String())
	header("To", strings.Join(to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+hex.EncodeToString(id[:])+"@"+domain+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	header("Auto-Submitted", "auto-generated")
	b.WriteString("\r\n")

	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return nil, fmt.Errorf("encode message: %w", err)
	}
	if err := qp.Close(); err != nil {
		return nil, fmt.Errorf("encode message: %w", err)
	}
	return b.Bytes(), nil
}
