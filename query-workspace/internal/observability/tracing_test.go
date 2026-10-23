package observability

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestHTTPTracePropagatesToOperationsAndLogsWithoutQueryData(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider, oldPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		otel.SetTextMapPropagator(oldPropagator)
		_ = provider.Shutdown(context.Background())
	})

	var logs bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&logs, nil))).With("app", "workspace")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /queries/{id}/run", func(w http.ResponseWriter, r *http.Request) {
		ctx, end := StartSpan(r.Context(), "neo4j.execute", trace.SpanKindClient)
		end(errors.New("private query text"))
		logger.ErrorContext(ctx, "execution failed")
		w.WriteHeader(http.StatusBadGateway)
	})
	request := httptest.NewRequest(http.MethodPost, "/queries/private-id/run?question=private-question", strings.NewReader("private-body"))
	request.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	response := httptest.NewRecorder()
	HTTPHandler(mux).ServeHTTP(response, request)

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want operation and request", len(spans))
	}
	child, server := spans[0], spans[1]
	if server.Name() != "POST /queries/{id}/run" || server.SpanKind() != trace.SpanKindServer {
		t.Fatalf("unexpected server span: %s (%s)", server.Name(), server.SpanKind())
	}
	if server.SpanContext().TraceID().String() != "0123456789abcdef0123456789abcdef" || child.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Fatal("request trace context was not propagated to the operation")
	}
	if child.Status().Code != codes.Error || server.Status().Code != codes.Error {
		t.Fatal("failed operation and HTTP response must be marked as errors")
	}
	for _, span := range spans {
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), "private") {
				t.Fatalf("query data leaked into attribute %s", attr.Key)
			}
		}
		if len(span.Events()) != 0 || strings.Contains(span.Status().Description, "private") {
			t.Fatal("raw error text leaked into a span")
		}
	}
	if !strings.Contains(logs.String(), `"trace_id":"0123456789abcdef0123456789abcdef"`) || !strings.Contains(logs.String(), `"span_id":`) {
		t.Fatalf("missing log correlation: %s", logs.String())
	}
}
