package generation

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/genai"
	"tpt-graph/query-workspace/internal/schema"
)

func TestDecodeGeneratedQuery(t *testing.T) {
	query, err := decodeGeneratedQuery(`{"cypher":"MATCH (n) RETURN n.name AS name","parameters":{"name":"example"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if query.Cypher != "MATCH (n) RETURN n.name AS name" || query.Parameters["name"] != "example" {
		t.Fatalf("decoded query = %#v", query)
	}
}

func TestDecodeGeneratedQueryPreservesNumericParameters(t *testing.T) {
	query, err := decodeGeneratedQuery(`{"cypher":"RETURN $id LIMIT $limit","parameters":{"limit":10,"id":9007199254740993,"nested":[{"min":-9223372036854775808,"score":0.25}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"limit": int64(10), "id": int64(9007199254740993),
		"nested": []any{map[string]any{"min": int64(-9223372036854775808), "score": float64(0.25)}},
	}
	if !reflect.DeepEqual(query.Parameters, want) {
		t.Fatalf("parameters = %#v, want %#v", query.Parameters, want)
	}
}

func TestDecodeGeneratedQueryRejectsUnexpectedFields(t *testing.T) {
	if _, err := decodeGeneratedQuery(`{"cypher":"RETURN 1","parameters":{},"explanation":"ignored"}`); err == nil {
		t.Fatal("expected unknown fields to be rejected")
	}
}

type testSchemaProvider struct {
	snapshot schema.Snapshot
	err      error
}

func (p *testSchemaProvider) Current(context.Context) (schema.Snapshot, error) {
	return p.snapshot, p.err
}

func TestGenerateUsesLatestCachedSchemaInEachVertexRequest(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old, oldPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(traces)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(old)
		otel.SetTextMapPropagator(oldPropagator)
		_ = traces.Shutdown(context.Background())
	})
	prompts := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Traceparent") == "" {
			t.Error("outbound Vertex request is missing trace context")
		}
		var body struct {
			SystemInstruction struct {
				Parts []struct{ Text string }
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if len(body.SystemInstruction.Parts) == 0 {
			t.Error("missing system instruction")
			http.Error(w, "missing prompt", http.StatusBadRequest)
			return
		}
		prompts <- body.SystemInstruction.Parts[0].Text
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"{\"cypher\":\"RETURN 1\",\"parameters\":{}}"}]}}]}`))
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Transport = otelhttp.NewTransport(httpClient.Transport)
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: "test-project", Location: "eu",
		HTTPClient: httpClient, HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &testSchemaProvider{}
	generator := NewVertexGenerator("test-project", "eu", "test-model", provider, slog.Default())
	generator.client = client
	for _, text := range []string{"Dependency {name: String}", "GitHubRepository {id: String}"} {
		provider.snapshot = schema.Snapshot{Text: text}
		if _, err := generator.Generate(context.Background(), "find dependencies"); err != nil {
			t.Fatal(err)
		}
		if prompt := <-prompts; !strings.Contains(prompt, text) {
			t.Fatalf("latest schema missing from Vertex prompt: %s", prompt)
		}
	}
	provider.err = schema.ErrUnavailable
	if _, err := generator.Generate(context.Background(), "find dependencies"); !errors.Is(err, schema.ErrUnavailable) {
		t.Fatalf("want missing schema error, got %v", err)
	}
	if len(prompts) != 0 {
		t.Fatal("Vertex was called without cached schema")
	}
	spans := recorder.Ended()
	if len(spans) != 5 || spans[1].Name() != "vertex.generate" || spans[4].Status().Code != codes.Error {
		t.Fatalf("expected generation, outbound HTTP and failure spans, got %v", spans)
	}
	for _, i := range []int{0, 2} {
		if spans[i].Parent().SpanID() != spans[i+1].SpanContext().SpanID() {
			t.Fatal("outbound HTTP span is not a child of generation")
		}
	}
}
