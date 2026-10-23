package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/genai"
	"tpt-graph/query-workspace/internal/schema"
	"tpt-graph/query-workspace/internal/workspace"
)

func TestGenerateClassifiesFailuresAndKeepsDiagnosticsWithoutPartialQuery(t *testing.T) {
	for _, test := range []struct {
		name, body, kind, finish, status string
		code                             int
	}{
		{"upstream timeout", `{"error":{"code":504,"status":"DEADLINE_EXCEEDED","message":"provider-internal-detail"}}`, "upstream_timeout", "", "DEADLINE_EXCEEDED", 504},
		{"output limit", `{"modelVersion":"model-1","usageMetadata":{"thoughtsTokenCount":4096},"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":"{\"cypher\":\"RETURN 1\",\"parameters\":{}}"}]}}]}`, "output_limit", "MAX_TOKENS", "", 200},
		{"prompt blocked", `{"promptFeedback":{"blockReason":"SAFETY"}}`, "blocked", "", "", 200},
		{"candidate blocked", `{"candidates":[{"finishReason":"SAFETY"}]}`, "blocked", "SAFETY", "", 200},
		{"other finish", `{"candidates":[{"finishReason":"OTHER"}]}`, "incomplete", "OTHER", "", 200},
		{"invalid response", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"not-json"}]}}]}`, "invalid_response", "STOP", "", 200},
		{"no candidates", `{}`, "empty_response", "", "", 200},
		{"quota", `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"provider-internal-detail"}}`, "rate_limited", "", "RESOURCE_EXHAUSTED", 429},
		{"unavailable", `{"error":{"code":503,"status":"UNAVAILABLE","message":"provider-internal-detail"}}`, "unavailable", "", "UNAVAILABLE", 503},
		{"bad request", `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"provider-internal-detail"}}`, "api_error", "", "INVALID_ARGUMENT", 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.code)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			httpClient := server.Client()
			httpClient.Transport = diagnosticTransport{base: httpClient.Transport}
			client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
				Backend: genai.BackendVertexAI, Project: "test-project", Location: "eu",
				HTTPClient: httpClient, HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
			})
			if err != nil {
				t.Fatal(err)
			}
			generator := NewVertexGenerator("test-project", "eu", "test-model",
				&testSchemaProvider{snapshot: schema.Snapshot{Text: "test schema"}}, slog.Default())
			generator.client = client
			query, err := generator.Generate(context.Background(), "find nodes")
			var failure *workspace.GenerationError
			if !errors.As(err, &failure) || failure.Kind != test.kind {
				t.Fatalf("expected %s, got %v", test.kind, err)
			}
			if query.Cypher != "" || query.Parameters != nil {
				t.Fatal("incomplete or blocked generation must not expose an executable query")
			}
			d := query.Diagnostics
			if d == nil || d.Outcome != test.kind || d.HTTPStatus != test.code || d.FinishReason != test.finish || d.APIStatus != test.status {
				t.Fatalf("failure diagnostics lost: %#v", d)
			}
			if strings.Contains(err.Error(), "provider-internal-detail") {
				t.Fatal("raw provider error exposed instead of helpful message")
			}
		})
	}
}

type failingTransport struct {
	err error
}

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func TestGenerateDistinguishesLocalTimeoutAndCancellation(t *testing.T) {
	for _, test := range []struct {
		err  error
		kind string
	}{
		{context.DeadlineExceeded, "local_timeout"},
		{context.Canceled, "canceled"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
				Backend: genai.BackendVertexAI, Project: "test-project", Location: "eu",
				HTTPClient: &http.Client{Transport: diagnosticTransport{base: failingTransport{err: test.err}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			generator := NewVertexGenerator("test-project", "eu", "test-model",
				&testSchemaProvider{snapshot: schema.Snapshot{Text: "test schema"}}, slog.Default())
			generator.client = client
			query, err := generator.Generate(context.Background(), "find nodes")
			var failure *workspace.GenerationError
			if !errors.As(err, &failure) || failure.Kind != test.kind || !errors.Is(err, test.err) {
				t.Fatalf("expected preserved %s error, got %v", test.kind, err)
			}
			if query.Diagnostics == nil || query.Diagnostics.Usage != nil || query.Diagnostics.Outcome != test.kind || query.Diagnostics.HTTPStatus != 0 {
				t.Fatalf("incorrect local failure metadata: %#v", query.Diagnostics)
			}
		})
	}
}

func TestGenerateReturnsResponseAndNetworkDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"modelVersion":"test-model-version","responseId":"response-123",
			"usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":25,"thoughtsTokenCount":10,"cachedContentTokenCount":80,"totalTokenCount":155},
			"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"{\"cypher\":\"RETURN 1\",\"parameters\":{}}"}]}}]
		}`))
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Transport = diagnosticTransport{base: httpClient.Transport}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: "test-project", Location: "eu",
		HTTPClient: httpClient, HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	generator := NewVertexGenerator("test-project", "eu", "test-model",
		&testSchemaProvider{snapshot: schema.Snapshot{Text: "private-schema", Hash: "schema-hash", CapturedAt: time.Now()}},
		slog.New(slog.NewJSONHandler(&logs, nil)))
	generator.client = client
	query, err := generator.Generate(context.Background(), "private-question")
	if err != nil {
		t.Fatal(err)
	}
	d := query.Diagnostics
	if d == nil || d.ModelVersion != "test-model-version" || d.ResponseID != "response-123" || d.FinishReason != "STOP" {
		t.Fatalf("missing response metadata: %#v", d)
	}
	if d.Usage == nil || d.Usage.PromptTokens != 120 || d.Usage.OutputTokens != 25 || d.Usage.ThinkingTokens != 10 || d.Usage.CachedTokens != 80 || d.Usage.TotalTokens != 155 {
		t.Fatalf("missing token usage: %#v", d)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].HTTPStatus != http.StatusOK || d.Attempts[0].TimeToFirstByteMS == nil || d.Attempts[0].RequestWriteMS == nil {
		t.Fatalf("missing network diagnostics: %#v", d)
	}
	if d.SchemaBytes != len("private-schema") || d.SchemaHash != "schema-hash" || d.ThinkingLevel != "LOW" || d.MaxOutputTokens != 4096 || d.Outcome != "success" {
		t.Fatalf("missing request metadata: %#v", d)
	}
	if strings.Contains(logs.String(), "private-question") || strings.Contains(logs.String(), "private-schema") || strings.Contains(logs.String(), "RETURN 1") {
		t.Fatal("content leaked into generation diagnostic logs")
	}
	if !strings.Contains(logs.String(), `"response_id":"response-123"`) {
		t.Fatalf("missing structured diagnostic log: %s", logs.String())
	}
}

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
