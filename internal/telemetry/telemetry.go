// Package telemetry sets up OpenTelemetry tracing and carries trace
// context through the job queue and over the agent connection, so a job's
// webhook, dispatch, steps and commit status end up in one trace.
package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Tracing is the process's tracer provider.
type Tracing struct {
	// Provider creates tracers. It is a no-op provider when tracing is off.
	Provider trace.TracerProvider
	// Endpoint is where spans are sent, without credentials; empty when
	// tracing is off.
	Endpoint string

	shutdown func(context.Context) error
}

// Enabled reports whether spans are exported.
func (t *Tracing) Enabled() bool {
	return t.Endpoint != ""
}

// Shutdown exports the spans still buffered and stops the provider.
func (t *Tracing) Shutdown(ctx context.Context) error {
	if t.shutdown == nil {
		return nil
	}
	if err := t.shutdown(ctx); err != nil {
		return fmt.Errorf("shut down tracing: %w", err)
	}
	return nil
}

// Setup exports spans over OTLP/HTTP when OTEL_EXPORTER_OTLP_TRACES_ENDPOINT
// or OTEL_EXPORTER_OTLP_ENDPOINT is set, and returns a no-op provider when
// neither is set or OTEL_SDK_DISABLED is true. service is the default
// service.name; OTEL_SERVICE_NAME overrides it.
//
// The SDK reads the other standard variables (OTEL_EXPORTER_OTLP_HEADERS,
// certificates, OTEL_TRACES_SAMPLER, OTEL_RESOURCE_ATTRIBUTES, ...) from
// the process environment.
func Setup(ctx context.Context, service string, getenv func(string) string) (*Tracing, error) {
	off := &Tracing{Provider: noop.NewTracerProvider()}
	if strings.EqualFold(getenv("OTEL_SDK_DISABLED"), "true") {
		return off, nil
	}
	endpoint := getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
	if endpoint == "" {
		base := getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
		if base == "" {
			return off, nil
		}
		// The base URL gets the signal's path, per the OTLP exporter spec.
		endpoint = strings.TrimRight(base, "/") + "/v1/traces"
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("OTLP endpoint %q must be an http:// or https:// URL", endpoint)
	}
	for _, key := range []string{"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL"} {
		if p := getenv(key); p != "" && p != "http/protobuf" {
			return nil, fmt.Errorf("%s=%s is not supported: only http/protobuf is", key, p)
		}
	}

	exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", service)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, fmt.Errorf("describe tracing resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	return &Tracing{Provider: tp, Endpoint: u.Redacted(), shutdown: tp.Shutdown}, nil
}

// propagator writes and reads W3C traceparent values.
var propagator = propagation.TraceContext{}

// TraceParent returns the W3C traceparent of the span in ctx, or "" when
// ctx has none. Stored with a job, it lets later work on the job join the
// trace that queued it.
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// WithTraceParent returns ctx with traceparent as the remote parent of the
// spans started from it. An empty or malformed value leaves ctx unchanged.
func WithTraceParent(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return propagator.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}

// Span attribute keys shared by the server and the agent.
const (
	AttrJobID       = attribute.Key("cisid.job.id")
	AttrRepo        = attribute.Key("cisid.repo")
	AttrBranch      = attribute.Key("cisid.branch")
	AttrCommit      = attribute.Key("cisid.commit")
	AttrPullRequest = attribute.Key("cisid.pull_request")
	AttrAgent       = attribute.Key("cisid.agent")
	AttrJobStatus   = attribute.Key("cisid.job.status")
	AttrStepIndex   = attribute.Key("cisid.step.index")
	AttrStepName    = attribute.Key("cisid.step.name")
	AttrStepImage   = attribute.Key("cisid.step.image")
	AttrExitCode    = attribute.Key("cisid.step.exit_code")
	AttrService     = attribute.Key("cisid.service.name")
	AttrMatrix      = attribute.Key("cisid.matrix")
	AttrDeliveryID  = attribute.Key("github.delivery_id")
)
