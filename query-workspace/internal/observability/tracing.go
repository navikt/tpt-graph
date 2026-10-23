package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func StartSpan(ctx context.Context, name string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, func(error)) {
	ctx, span := otel.Tracer("tpt-graph/query-workspace").Start(ctx, name,
		trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
	return ctx, func(err error) {
		if err != nil {
			RecordError(ctx, err)
		}
		span.End()
	}
}

func RecordError(ctx context.Context, err error) {
	span := trace.SpanFromContext(ctx)
	errorType := fmt.Sprintf("%T", err)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		errorType = "timeout"
	case errors.Is(err, context.Canceled):
		errorType = "canceled"
	}
	span.SetAttributes(attribute.String("error.type", errorType))
	span.SetStatus(codes.Error, "operation failed")
}

func HTTPHandler(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		name := pattern
		if name == "" {
			name = r.Method
		}
		route := pattern
		if _, path, ok := strings.Cut(pattern, " "); ok {
			route = path
		}
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("tpt-graph/query-workspace").Start(ctx, name,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.request.method", r.Method), attribute.String("http.route", route)))
		defer span.End()
		response := &traceResponseWriter{ResponseWriter: w, status: http.StatusOK}
		completed := false
		defer func() {
			span.SetAttributes(attribute.Int("http.response.status_code", response.status))
			if !completed || response.status >= 500 {
				span.SetStatus(codes.Error, "request failed")
			}
		}()
		mux.ServeHTTP(response, r.WithContext(ctx))
		completed = true
	})
}

type traceResponseWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *traceResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.ResponseWriter.WriteHeader(status)
	if status >= 100 && status < 200 {
		return
	}
	w.status = status
	w.wroteHeader = true
}

func (w *traceResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *traceResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type traceHandler struct {
	slog.Handler
}

func NewTraceHandler(handler slog.Handler) slog.Handler {
	return &traceHandler{Handler: handler}
}

func (h *traceHandler) Handle(ctx context.Context, record slog.Record) error {
	span := trace.SpanContextFromContext(ctx)
	if span.IsValid() {
		record.AddAttrs(slog.String("trace_id", span.TraceID().String()), slog.String("span_id", span.SpanID().String()))
	}
	return h.Handler.Handle(ctx, record)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return NewTraceHandler(h.Handler.WithAttrs(attrs))
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
	return NewTraceHandler(h.Handler.WithGroup(name))
}
