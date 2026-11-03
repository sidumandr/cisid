// Package telemetrytest provides an in-process OTLP/HTTP collector, so
// tests can check the spans a process really exports.
package telemetrytest

import (
	"compress/gzip"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Span is an exported span, flattened for assertions.
type Span struct {
	Service  string
	Name     string
	TraceID  string // hex
	SpanID   string // hex
	ParentID string // hex; empty for a root span
	Attrs    map[string]string
	Error    bool
}

// Collector accepts OTLP/HTTP trace exports and keeps the spans.
type Collector struct {
	srv *httptest.Server

	mu    sync.Mutex
	spans []Span
}

// NewCollector starts a collector that is closed when the test ends.
func NewCollector(t testing.TB) *Collector {
	t.Helper()
	c := &Collector{}
	c.srv = httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(c.srv.Close)
	return c
}

// URL is the collector's base URL, a value for OTEL_EXPORTER_OTLP_ENDPOINT.
func (c *Collector) URL() string {
	return c.srv.URL
}

// Spans returns the spans received so far.
func (c *Collector) Spans() []Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Span(nil), c.spans...)
}

// Find returns the first span received with the given name.
func (c *Collector) Find(name string) (Span, bool) {
	for _, s := range c.Spans() {
		if s.Name == name {
			return s, true
		}
	}
	return Span{}, false
}

func (c *Collector) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/traces" {
		http.NotFound(w, r)
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = zr
	}
	data, err := io.ReadAll(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(data, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var spans []Span
	for _, rs := range req.GetResourceSpans() {
		service := attrs(rs.GetResource().GetAttributes())["service.name"]
		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				spans = append(spans, Span{
					Service:  service,
					Name:     s.GetName(),
					TraceID:  hex.EncodeToString(s.GetTraceId()),
					SpanID:   hex.EncodeToString(s.GetSpanId()),
					ParentID: hex.EncodeToString(s.GetParentSpanId()),
					Attrs:    attrs(s.GetAttributes()),
					Error:    s.GetStatus().GetCode() == tracepb.Status_STATUS_CODE_ERROR,
				})
			}
		}
	}
	c.mu.Lock()
	c.spans = append(c.spans, spans...)
	c.mu.Unlock()

	resp, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(resp)
}

func attrs(kvs []*commonpb.KeyValue) map[string]string {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		v := kv.GetValue()
		switch v.GetValue().(type) {
		case *commonpb.AnyValue_IntValue:
			m[kv.GetKey()] = strconv.FormatInt(v.GetIntValue(), 10)
		case *commonpb.AnyValue_BoolValue:
			m[kv.GetKey()] = strconv.FormatBool(v.GetBoolValue())
		default:
			m[kv.GetKey()] = v.GetStringValue()
		}
	}
	return m
}
