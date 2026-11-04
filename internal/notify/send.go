package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/sidumandr/cisid/pkg/models"
)

// temporaryError is a failure that may pass, worth another attempt.
type temporaryError struct{ msg string }

func (e *temporaryError) Error() string { return e.msg }

func temporary(format string, args ...any) error {
	return &temporaryError{msg: fmt.Sprintf(format, args...)}
}

func isTemporary(err error) bool {
	var t *temporaryError
	return errors.As(err, &t)
}

// metadataAddrs are cloud metadata services outside the link-local range.
var metadataAddrs = []netip.Addr{
	netip.MustParseAddr("fd00:ec2::254"),   // AWS over IPv6
	netip.MustParseAddr("100.100.100.200"), // Alibaba Cloud
}

// errRefusedAddress is returned for a connection to an address
// notifications may not reach.
var errRefusedAddress = errors.New("address not allowed for notifications")

// refuseAddresses keeps notifications away from link-local, multicast and
// unspecified addresses and cloud metadata services, which a URL a user
// typed must not reach. Private networks stay allowed: chats and webhooks
// are often self-hosted.
func refuseAddresses(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", errRefusedAddress, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("%w: %s", errRefusedAddress, address)
	}
	ip = ip.Unmap()
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("%w: %s", errRefusedAddress, ip)
	}
	for _, m := range metadataAddrs {
		if ip == m {
			return fmt.Errorf("%w: %s", errRefusedAddress, ip)
		}
	}
	return nil
}

// SafeHTTPClient returns the client chats and webhooks are posted with. It
// does not connect to the addresses refuseAddresses refuses and does not
// follow redirects, so a notification goes only where its URL says.
func SafeHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: refuseAddresses}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Transport: transport,
		Timeout:   sendTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// post sends body to rawURL. Errors name only the URL's host: the rest of
// a chat's webhook URL is its credential.
func (s *Service) post(ctx context.Context, rawURL string, body []byte, header http.Header) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("the url is invalid")
	}
	host := u.Host
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("the url of %s is invalid", host)
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cisid")

	resp, err := s.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // without the URL
		}
		if errors.Is(err, errRefusedAddress) {
			return fmt.Errorf("post to %s: %w", host, err)
		}
		return temporary("post to %s: %v", host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return temporary("%s answered %s", host, resp.Status)
	}
	return fmt.Errorf("%s answered %s", host, resp.Status)
}

// postSlack posts msg as a Slack incoming webhook message.
func (s *Service) postSlack(ctx context.Context, t Target, msg Message) error {
	// Slack's own markup uses < and >, which need not be escaped again.
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]string{"text": msg.Slack()}); err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	return s.post(ctx, t.URL, body.Bytes(), nil)
}

// WebhookBody is the JSON a webhook notification posts.
type WebhookBody struct {
	// Result is failed, broken, fixed, passed, or test.
	Result string `json:"result"`
	Repo   string `json:"repo"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// URL is the job's page; empty without CISID_PUBLIC_URL.
	URL string `json:"url,omitempty"`
	// Job is the job, as the job API returns it; absent in a test.
	Job *models.Job `json:"job,omitempty"`
}

// Signature returns the X-Cisid-Signature of body under secret.
func Signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// postWebhook posts msg as JSON, signed when t has a secret.
func (s *Service) postWebhook(ctx context.Context, t Target, msg Message) error {
	body, err := json.Marshal(WebhookBody{
		Result: msg.Result, Repo: msg.Repo, Title: msg.Title, Detail: msg.Detail, URL: msg.URL, Job: msg.Job,
	})
	if err != nil {
		return fmt.Errorf("encode message: %w", err)
	}
	header := http.Header{"X-Cisid-Result": {msg.Result}}
	if t.Secret != "" {
		header.Set("X-Cisid-Signature", Signature(t.Secret, body))
	}
	return s.post(ctx, t.URL, body, header)
}
