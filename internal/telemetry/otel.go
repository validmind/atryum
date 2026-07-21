// Package telemetry wires OpenTelemetry trace export for Atryum. It installs a
// global tracer provider that fans the same spans out to every configured OTLP
// endpoint (e.g. Langfuse and Datadog at once) with no vendor-specific code —
// each backend is just a URL + headers. When disabled the provider is a no-op,
// so instrumentation sprinkled through the codebase costs nothing.
package telemetry

import (
	"context"
	"encoding/base64"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"atryum/internal/config"
)

func noopShutdown(context.Context) error { return nil }

// buildResource stamps the identity attributes every span carries. service.name
// and deployment.environment are what Datadog maps to `service` and `env`.
func buildResource(environment string) *resource.Resource {
	return resource.NewSchemaless(
		attribute.String("service.name", "atryum"),
		attribute.String("deployment.environment", environment),
	)
}

// Setup installs a global tracer provider fanning out to every configured OTLP
// exporter and returns a shutdown func that flushes buffered spans. When
// cfg.Enabled is false it installs nothing and returns a no-op shutdown.
//
// environment is atryum's resolved instance identity (atryum_instance, else
// public_base_url; see cmd/atryum): it becomes the deployment.environment
// resource attribute, which Datadog surfaces as the `env` tag. Langfuse project
// selection is driven purely by the per-exporter key pair, not by this value.
func Setup(ctx context.Context, cfg config.OTELConfig, environment string) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return noopShutdown, nil
	}
	if len(cfg.Exporters) == 0 {
		return nil, fmt.Errorf("otel.enabled is true but no [[otel.exporters]] are configured")
	}

	// Sample everything (the SDK default). Add a sample-rate knob only
	// if trace volume ever becomes a cost problem.
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(buildResource(environment)),
	}
	for _, ex := range cfg.Exporters {
		exp, err := newExporter(ctx, ex)
		if err != nil {
			return nil, fmt.Errorf("otel exporter %q: %w", ex.Name, err)
		}
		opts = append(opts, sdktrace.WithBatcher(exp))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	// Accept inbound W3C trace context so Atryum spans can nest under a calling
	// agent's trace when the header is propagated.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp.Shutdown, nil
}

// newExporter builds one OTLP/HTTP span exporter. The endpoint must be the full
// OTLP traces URL including the signal path (e.g. Langfuse
// ".../api/public/otel/v1/traces") — it is used verbatim, no path is appended.
// Its scheme sets transport security: http → plaintext, https → TLS.
func newExporter(ctx context.Context, ex config.OTLPExporterConfig) (sdktrace.SpanExporter, error) {
	if ex.Endpoint == "" {
		return nil, fmt.Errorf("endpoint is required")
	}
	opts := []otlptracehttp.Option{otlptracehttp.WithEndpointURL(ex.Endpoint)}
	if headers := exporterHeaders(ex); len(headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(headers))
	}
	return otlptracehttp.New(ctx, opts...)
}

// exporterHeaders resolves the outgoing headers for an exporter, adding a
// Basic-auth header computed from a public/secret key pair (Langfuse) when both
// are set and no explicit Authorization header was provided.
func exporterHeaders(ex config.OTLPExporterConfig) map[string]string {
	headers := make(map[string]string, len(ex.Headers)+1)
	for k, v := range ex.Headers {
		headers[k] = v
	}
	if _, ok := headers["Authorization"]; !ok && ex.PublicKey != "" && ex.SecretKey != "" {
		token := base64.StdEncoding.EncodeToString([]byte(ex.PublicKey + ":" + ex.SecretKey))
		headers["Authorization"] = "Basic " + token
	}
	return headers
}
