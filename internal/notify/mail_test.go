package notify

import (
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

// smtpRelay is a minimal SMTP server that keeps the messages it accepts.
type smtpRelay struct {
	ln        net.Listener
	startTLS  bool   // advertise STARTTLS (it is never carried out)
	rcptReply string // answer to RCPT TO, e.g. "550 no such user"; empty accepts
	mu        sync.Mutex
	messages  []relayed
	wg        sync.WaitGroup
}

type relayed struct {
	from string
	to   []string
	data string
}

func newRelay(t *testing.T, configure func(*smtpRelay)) *smtpRelay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	r := &smtpRelay{ln: ln}
	if configure != nil {
		configure(r)
	}
	r.wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			r.wg.Go(func() { r.serve(conn) })
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		r.wg.Wait()
	})
	return r
}

func (r *smtpRelay) port() int { return r.ln.Addr().(*net.TCPAddr).Port }

func (r *smtpRelay) serve(conn net.Conn) {
	tp := textproto.NewConn(conn)
	defer func() { _ = tp.Close() }()
	_ = tp.PrintfLine("220 relay.test ESMTP")
	var msg relayed
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"):
			if r.startTLS {
				_ = tp.PrintfLine("250-relay.test")
				_ = tp.PrintfLine("250 STARTTLS")
			} else {
				_ = tp.PrintfLine("250 relay.test")
			}
		case strings.HasPrefix(verb, "HELO"), strings.HasPrefix(verb, "RSET"), strings.HasPrefix(verb, "NOOP"):
			_ = tp.PrintfLine("250 OK")
		case strings.HasPrefix(verb, "MAIL FROM:"):
			msg = relayed{from: strings.Trim(line[len("MAIL FROM:"):], "<> ")}
			_ = tp.PrintfLine("250 OK")
		case strings.HasPrefix(verb, "RCPT TO:"):
			if r.rcptReply != "" {
				_ = tp.PrintfLine("%s", r.rcptReply)
				continue
			}
			msg.to = append(msg.to, strings.Trim(line[len("RCPT TO:"):], "<> "))
			_ = tp.PrintfLine("250 OK")
		case verb == "DATA":
			_ = tp.PrintfLine("354 go ahead")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			msg.data = string(data)
			r.mu.Lock()
			r.messages = append(r.messages, msg)
			r.mu.Unlock()
			_ = tp.PrintfLine("250 queued")
		case verb == "QUIT":
			_ = tp.PrintfLine("221 bye")
			return
		default:
			_ = tp.PrintfLine("502 not implemented")
		}
	}
}

func (r *smtpRelay) received() []relayed {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]relayed(nil), r.messages...)
}

func relayMailer(t *testing.T, r *smtpRelay, mode string) *Mailer {
	t.Helper()
	m, err := NewMailer(MailConfig{Host: "127.0.0.1", Port: r.port(), From: "cisid <ci@example.com>", TLS: mode})
	require.NoError(t, err)
	return m
}

func TestMailerSends(t *testing.T) {
	r := newRelay(t, nil)
	m := relayMailer(t, r, TLSNone)

	err := m.Send(t.Context(), []string{"dev@example.com", "ops@example.com"},
		"[cisid] main is broken\r\nBcc: someone@example.org", "Line one ünicode\nLine two\n")
	require.NoError(t, err)

	got := r.received()
	require.Len(t, got, 1)
	assert.Equal(t, "ci@example.com", got[0].from)
	assert.Equal(t, []string{"dev@example.com", "ops@example.com"}, got[0].to)

	parsed, err := mail.ReadMessage(strings.NewReader(got[0].data))
	require.NoError(t, err)
	assert.Empty(t, parsed.Header.Get("Bcc"), "a line break in the subject cannot add a header")
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	require.NoError(t, err)
	assert.Equal(t, "[cisid] main is broken\r\nBcc: someone@example.org", subject)
	assert.Equal(t, `"cisid" <ci@example.com>`, parsed.Header.Get("From"))
	assert.Equal(t, "dev@example.com, ops@example.com", parsed.Header.Get("To"))
	assert.Equal(t, "auto-generated", parsed.Header.Get("Auto-Submitted"))
	assert.True(t, strings.HasSuffix(parsed.Header.Get("Message-ID"), "@example.com>"))
	body, err := io.ReadAll(quotedprintable.NewReader(parsed.Body))
	require.NoError(t, err)
	assert.Equal(t, "Line one ünicode\nLine two\n", string(body), "the relay reads lines without CR")
}

func TestMailerNeedsStartTLS(t *testing.T) {
	r := newRelay(t, nil)
	err := relayMailer(t, r, TLSStartTLS).Send(t.Context(), []string{"dev@example.com"}, "s", "b")
	require.ErrorIs(t, err, errNoStartTLS, "never sends in the clear when TLS is asked for")
	assert.False(t, isTemporary(err))
	assert.Empty(t, r.received())
}

func TestMailerRejections(t *testing.T) {
	permanent := newRelay(t, func(r *smtpRelay) { r.rcptReply = "550 no such user" })
	err := relayMailer(t, permanent, TLSNone).Send(t.Context(), []string{"x@example.com"}, "s", "b")
	require.Error(t, err)
	assert.False(t, isTemporary(err), "5xx is final")
	assert.Contains(t, err.Error(), "no such user")

	later := newRelay(t, func(r *smtpRelay) { r.rcptReply = "451 try again later" })
	err = relayMailer(t, later, TLSNone).Send(t.Context(), []string{"x@example.com"}, "s", "b")
	require.Error(t, err)
	assert.True(t, isTemporary(err), "4xx may pass")
}

func TestNewMailerChecks(t *testing.T) {
	for _, cfg := range []MailConfig{
		{Host: "mail.example.com", Port: 587, From: "not an address", TLS: TLSStartTLS},
		{Host: "mail.example.com", Port: 587, From: "ci@example.com", TLS: "ssl"},
		{Port: 587, From: "ci@example.com", TLS: TLSStartTLS},
	} {
		_, err := NewMailer(cfg)
		assert.Error(t, err, cfg)
	}
}

func TestEmailNotification(t *testing.T) {
	r := newRelay(t, nil)
	store := newMemStore()
	s := newService(t, store, WithMailer(relayMailer(t, r, TLSNone)))
	require.NoError(t, s.Put(t.Context(), models.Notification{Repo: repo, Name: "team", Kind: models.NotifyEmail},
		Target{To: []string{"dev@example.com"}}))

	store.finish(finished(12, models.JobStatusFailed, "main"))
	s.round(t.Context())

	got := r.received()
	require.Len(t, got, 1)
	parsed, err := mail.ReadMessage(strings.NewReader(got[0].data))
	require.NoError(t, err)
	subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	require.NoError(t, err)
	assert.Equal(t, "[cisid] sidumandr/cisid: main is broken", subject)
	body, err := io.ReadAll(quotedprintable.NewReader(parsed.Body))
	require.NoError(t, err)
	assert.Contains(t, string(body), "https://ci.example.com/jobs/12")
	assert.Contains(t, string(body), "Build #12 · commit 6113728 · push")
	assert.Equal(t, []string{""}, store.recorded("team"))
}
