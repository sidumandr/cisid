package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

// RequestTimeout bounds each API request of the default HTTP client.
const RequestTimeout = 15 * time.Second

// API sends requests to a forge's REST API.
type API struct {
	// Base is the API's base URL, without a trailing slash.
	Base string
	// HTTP sends the requests; a client with RequestTimeout when nil.
	HTTP *http.Client
	// Authorize adds credentials to a request; nil sends none.
	Authorize func(*http.Request)
	// Header is set on every request, e.g. an API version.
	Header http.Header
}

// Do sends a request to Base+path. A non-nil body is sent as JSON. A
// transport failure is ErrTemporary; the response status is not checked.
func (a API) Do(ctx context.Context, method, path, accept string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.Base+path, r)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range a.Header {
		req.Header[k] = v
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", "cisid")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.Authorize != nil {
		a.Authorize(req)
	}

	client := a.HTTP
	if client == nil {
		client = &http.Client{Timeout: RequestTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTemporary, err)
	}
	return resp, nil
}

// GetJSON decodes the JSON answer of a GET request into v.
func (a API) GetJSON(ctx context.Context, path string, v any) error {
	resp, err := a.Do(ctx, http.MethodGet, path, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := CheckStatus(resp); err != nil {
		return err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxFileBytes)).Decode(v); err != nil {
		return fmt.Errorf("%w: decode answer: %w", ErrTemporary, err)
	}
	return nil
}

// Send sends body with method and only checks the answer's status.
func (a API) Send(ctx context.Context, method, path string, body any) error {
	resp, err := a.Do(ctx, method, path, "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return CheckStatus(resp)
}

// File returns the raw body of a GET request, up to MaxFileBytes.
func (a API) File(ctx context.Context, path, accept string) ([]byte, error) {
	resp, err := a.Do(ctx, http.MethodGet, path, accept, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := CheckStatus(resp); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read file: %w", ErrTemporary, err)
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooLarge, MaxFileBytes)
	}
	return data, nil
}

// CheckStatus maps a non-2xx response to ErrNotFound, ErrTemporary or a
// permanent error carrying the forge's message.
func CheckStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))

	// GitHub and GitLab answer 403 when the rate limit is used up.
	limited := resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("RateLimit-Remaining") == "0"
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusForbidden && limited,
		resp.StatusCode >= 500:
		return fmt.Errorf("%w: %s: %s", ErrTemporary, resp.Status, msg)
	default:
		return fmt.Errorf("unexpected response %s: %s", resp.Status, msg)
	}
}

// EscapePath escapes each segment of a slash-separated path.
func EscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// TracedHTTPClient returns an HTTP client that records each request as a
// client span of tp. No trace headers are sent to the forge.
func TracedHTTPClient(tp trace.TracerProvider) *http.Client {
	return &http.Client{
		Timeout:   RequestTimeout,
		Transport: otelhttp.NewTransport(http.DefaultTransport, otelhttp.WithTracerProvider(tp)),
	}
}

// WebURL checks a forge's base URL setting and returns it without a
// trailing slash.
func WebURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("%q must be an http(s) URL", raw)
	}
	return strings.TrimSuffix(raw, "/"), nil
}
