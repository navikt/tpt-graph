package observability

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

type traceCollector struct {
	collector.UnimplementedTraceServiceServer
	requests chan *collector.ExportTraceServiceRequest
}

func (c *traceCollector) Export(_ context.Context, request *collector.ExportTraceServiceRequest) (*collector.ExportTraceServiceResponse, error) {
	c.requests <- request
	return &collector.ExportTraceServiceResponse{}, nil
}

func TestTracingExportsToConfiguredNaisCollectorOnShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	sink := &traceCollector{requests: make(chan *collector.ExportTraceServiceRequest, 1)}
	collector.RegisterTraceServiceServer(server, sink)
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(server.Stop)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+listener.Addr().String())
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_SERVICE_NAME", "tpt-query-workspace")
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "service.namespace=appsec")
	t.Setenv("OTEL_TRACES_SAMPLER", "always_on")
	oldProvider, oldPropagator, oldErrors := otel.GetTracerProvider(), otel.GetTextMapPropagator(), otel.GetErrorHandler()
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		otel.SetTextMapPropagator(oldPropagator)
		otel.SetErrorHandler(oldErrors)
	})

	shutdown, err := SetupTracing(context.Background(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() { _ = shutdown(ctx) })
	_, end := StartSpan(ctx, "test.operation", trace.SpanKindInternal)
	end(nil)
	if err := shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-sink.requests:
		if len(request.ResourceSpans) != 1 {
			t.Fatalf("unexpected exported resources: %v", request.ResourceSpans)
		}
		resource := request.ResourceSpans[0]
		foundService, foundNamespace := false, false
		for _, attr := range resource.Resource.Attributes {
			foundService = foundService || attr.Key == "service.name" && attr.Value.GetStringValue() == "tpt-query-workspace"
			foundNamespace = foundNamespace || attr.Key == "service.namespace" && attr.Value.GetStringValue() == "appsec"
		}
		if !foundService || !foundNamespace || len(resource.ScopeSpans) != 1 || len(resource.ScopeSpans[0].Spans) != 1 {
			t.Fatalf("missing Nais resource attributes or span: %v", resource)
		}
	case <-ctx.Done():
		t.Fatal("trace was not exported to the configured collector")
	}
}
