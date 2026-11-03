package telemetry

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/sidumandr/cisid/internal/telemetry/telemetrytest"
)

const testTimeout = 5 * time.Second

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSetupIsOffWithoutEndpoint(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"no endpoint":  {},
		"sdk disabled": {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4318", "OTEL_SDK_DISABLED": "true"},
	} {
		t.Run(name, func(t *testing.T) {
			tr, err := Setup(t.Context(), "cisid-test", env(vars))
			require.NoError(t, err)
			assert.False(t, tr.Enabled())

			_, span := tr.Provider.Tracer("test").Start(t.Context(), "op")
			assert.False(t, span.SpanContext().IsValid(), "a disabled provider records nothing")
			span.End()
			assert.NoError(t, tr.Shutdown(t.Context()))
		})
	}
}

func TestSetupRejectsBadConfig(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"grpc protocol":   {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:4317", "OTEL_EXPORTER_OTLP_PROTOCOL": "grpc"},
		"not a URL":       {"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "collector:4318"},
		"unknown scheme":  {"OTEL_EXPORTER_OTLP_ENDPOINT": "ftp://collector"},
		"traces protocol": {"OTEL_EXPORTER_OTLP_ENDPOINT": "http://c:4318", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "http/json"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Setup(t.Context(), "cisid-test", env(vars))
			assert.Error(t, err)
		})
	}
}

// TestSetupExportsToCollector sends a span to a real OTLP/HTTP endpoint,
// once through the base URL variable and once through the traces URL.
func TestSetupExportsToCollector(t *testing.T) {
	for _, key := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		t.Run(key, func(t *testing.T) {
			col := telemetrytest.NewCollector(t)
			endpoint := col.URL()
			if key == "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT" {
				endpoint += "/v1/traces"
			}
			tr, err := Setup(t.Context(), "cisid-test", env(map[string]string{key: endpoint}))
			require.NoError(t, err)
			assert.True(t, tr.Enabled())
			assert.Equal(t, col.URL()+"/v1/traces", tr.Endpoint)

			_, span := tr.Provider.Tracer("test").Start(t.Context(), "op")
			span.SetAttributes(AttrJobID.Int64(7))
			span.End()

			ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
			defer cancel()
			require.NoError(t, tr.Shutdown(ctx), "shutdown flushes the span")
			got, ok := col.Find("op")
			require.True(t, ok, "the collector received the span")
			assert.Equal(t, "cisid-test", got.Service)
			assert.Equal(t, "7", got.Attrs["cisid.job.id"])
		})
	}
}

func TestSetupHidesEndpointCredentials(t *testing.T) {
	tr, err := Setup(t.Context(), "cisid-test", env(map[string]string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "https://user:secret@collector.example.com/v1/traces",
	}))
	require.NoError(t, err)
	defer func() { _ = tr.Shutdown(t.Context()) }()
	assert.NotContains(t, tr.Endpoint, "secret")
	assert.Contains(t, tr.Endpoint, "collector.example.com")
}

func recordingProvider(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, rec
}

func TestTraceParentCarriesTheTrace(t *testing.T) {
	tp, rec := recordingProvider(t)
	tracer := tp.Tracer("test")

	ctx, parent := tracer.Start(t.Context(), "webhook")
	tp1 := TraceParent(ctx)
	parent.End()
	require.NotEmpty(t, tp1)
	assert.True(t, strings.HasPrefix(tp1, "00-"+parent.SpanContext().TraceID().String()+"-"), tp1)

	// Later, somewhere else: a fresh context continues the stored trace.
	_, child := tracer.Start(WithTraceParent(context.Background(), tp1), "dispatch")
	child.End()

	spans := rec.Ended()
	require.Len(t, spans, 2)
	assert.Equal(t, parent.SpanContext().TraceID(), spans[1].SpanContext().TraceID())
	assert.Equal(t, parent.SpanContext().SpanID(), spans[1].Parent().SpanID())
	assert.True(t, spans[1].Parent().IsRemote())
}

func TestTraceParentWithoutSpan(t *testing.T) {
	assert.Empty(t, TraceParent(context.Background()))

	for _, bad := range []string{"", "garbage", "00-00000000000000000000000000000000-0000000000000000-01"} {
		ctx := WithTraceParent(context.Background(), bad)
		assert.False(t, trace.SpanContextFromContext(ctx).IsValid(), bad)
	}
}

func TestLogHandlerAddsTraceIDs(t *testing.T) {
	tp, _ := recordingProvider(t)
	var buf bytes.Buffer
	log := slog.New(LogHandler(slog.NewTextHandler(&buf, nil))).With("component", "test")

	log.InfoContext(context.Background(), "no span")
	assert.NotContains(t, buf.String(), "trace_id")

	ctx, span := tp.Tracer("test").Start(t.Context(), "op")
	defer span.End()
	buf.Reset()
	log.InfoContext(ctx, "in span")
	out := buf.String()
	assert.Contains(t, out, "trace_id="+span.SpanContext().TraceID().String())
	assert.Contains(t, out, "span_id="+span.SpanContext().SpanID().String())
	assert.Contains(t, out, "component=test", "attributes from With are kept")
}
