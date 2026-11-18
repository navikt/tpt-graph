package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
	"tpt-graph/query-workspace/internal/modeldiag"
	"tpt-graph/query-workspace/internal/schema"
	"tpt-graph/query-workspace/internal/workspace"
)

const chatSchema = "Node properties (observed in the database; properties may be optional):\n" +
	"`GitHubBranch` {`name`: String}\n`GitHubRepository` {`name`: String}\n`UnrelatedCloudType` {`private_metadata`: String}\n" +
	"\nRelationship properties (observed in the database):\n`BRANCH` {`lastupdated`: Long}\n" +
	"\nDirected relationships (metadata-inferred; not verified against actual edges):\n" +
	"(:`GitHubRepository`)-[:`BRANCH`]->(:`GitHubBranch`)\n" +
	"Metadata does not establish business meaning or prove an attack path.\n"

func TestSchemaChatAnswersFromFullSchemaInOneRequest(t *testing.T) {
	requests := 0
	answer := "The snapshot includes GitHubBranch and a metadata-inferred BRANCH relationship."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if !strings.Contains(r.URL.Path, "/models/test-chat-model:generateContent") {
			t.Errorf("chat did not use its independent model: %s", r.URL.Path)
		}
		var request struct {
			SystemInstruction *genai.Content
			Contents          []*genai.Content
			Tools             []*genai.Tool
			ToolConfig        *genai.ToolConfig
			GenerationConfig  genai.GenerateContentConfig
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		body, _ := json.Marshal(request)
		if len(request.Tools) != 0 || request.ToolConfig != nil {
			t.Error("full-schema chat must not expose any tools")
		}
		if request.SystemInstruction == nil || len(request.SystemInstruction.Parts) == 0 ||
			!strings.Contains(request.SystemInstruction.Parts[0].Text, chatSchema) {
			t.Error("the first request must include the complete cached schema")
		}
		config := request.GenerationConfig
		if config.ThinkingConfig == nil || config.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelMedium {
			t.Error("chat must use medium thinking on every model request")
		}
		if config.MaxOutputTokens != 16384 || config.ResponseMIMEType != "application/json" || config.ResponseSchema == nil {
			t.Error("chat must request a bounded structured answer with schema references")
		}
		if len(request.Contents) != 3 || !strings.Contains(string(body), "earlier-question") ||
			!strings.Contains(string(body), "Do we have branch information?") {
			t.Error("conversation or current question was lost")
		}
		for _, private := range []string{"private-history-diagnostics", "private-result-marker"} {
			if strings.Contains(string(body), private) {
				t.Errorf("non-conversation context reached the model: %s", private)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"modelVersion":"chat-version","responseId":"response-1","usageMetadata":{"promptTokenCount":120,"candidatesTokenCount":25,"thoughtsTokenCount":10,"totalTokenCount":155},"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":%q}]}}]}`,
			fmt.Sprintf(`{"text":%q,"labels":["GitHubBranch"]}`, answer))
	}))
	defer server.Close()
	captured := time.Now().UTC()
	generator := chatGenerator(t, server, schema.Snapshot{Text: chatSchema, Hash: "schema-hash", CapturedAt: captured})
	var logs bytes.Buffer
	generator.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	reply, err := generator.Chat(context.Background(), []schema.Turn{
		{Question: "earlier-question", Reply: schema.Reply{Text: "Earlier answer.",
			Evidence:    schema.Evidence{Nodes: []schema.NodeType{{Name: "private-result-marker"}}},
			Diagnostics: &modeldiag.Chat{Diagnostics: modeldiag.Diagnostics{TraceID: "private-history-diagnostics"}}}},
	}, "Do we have branch information?")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || reply.Text != answer || len(reply.Evidence.Nodes) != 2 ||
		len(reply.Evidence.Relationships) != 1 || !reply.CapturedAt.Equal(captured) {
		t.Fatalf("ungrounded or incomplete answer: requests=%d reply=%#v", requests, reply)
	}
	d := reply.Diagnostics
	if d == nil || d.ThinkingLevel != "MEDIUM" || d.Outcome != "success" || d.HistoryTurns != 1 || d.ModelRequests != 1 || d.SchemaContext != "full" ||
		len(d.Attempts) != 1 || d.DurationMS <= 0 || d.SchemaCaptured == nil || !d.SchemaCaptured.Equal(captured) ||
		d.SchemaBytes != len(chatSchema) || d.SchemaHash != "schema-hash" || d.SystemBytes <= len(chatSchema) {
		t.Fatalf("missing chat diagnostics: %#v", d)
	}
	if d.Usage == nil || d.Usage.TotalTokens != 155 || d.Usage.ThinkingTokens != 10 ||
		d.ModelVersion != "chat-version" || d.ResponseID != "response-1" || d.HTTPStatus != 200 {
		t.Fatalf("response diagnostics lost: %#v", d)
	}
	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"GitHubBranch", "UnrelatedCloudType", "earlier-question", answer} {
		if strings.Contains(string(encoded), private) || strings.Contains(logs.String(), private) {
			t.Errorf("content leaked into diagnostics: %s", private)
		}
	}
}

func TestSchemaChatFollowUpUsesCurrentFullSnapshot(t *testing.T) {
	requests := 0
	updated := strings.ReplaceAll(chatSchema, "GitHubBranch", "NewBranch")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			SystemInstruction *genai.Content
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		snapshot, label := chatSchema, "GitHubBranch"
		if requests > 0 {
			snapshot, label = updated, "NewBranch"
		}
		if request.SystemInstruction == nil || len(request.SystemInstruction.Parts) == 0 ||
			!strings.Contains(request.SystemInstruction.Parts[0].Text, snapshot) {
			t.Error("follow-up did not receive the current full schema")
		}
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, chatModelResponse(fmt.Sprintf(`{"text":"Metadata answer","labels":[%q]}`, label)))
	}))
	defer server.Close()
	provider := &testSchemaProvider{snapshot: schema.Snapshot{Text: chatSchema, Hash: "first"}}
	generator := chatGenerator(t, server, provider.snapshot)
	generator.schemas = provider
	first, err := generator.Chat(context.Background(), nil, "branches?")
	if err != nil {
		t.Fatal(err)
	}
	provider.snapshot = schema.Snapshot{Text: updated, Hash: "second"}
	second, err := generator.Chat(context.Background(), []schema.Turn{{Question: "branches?", Reply: first}}, "What changed?")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || second.Diagnostics.SchemaHash != "second" || len(second.Evidence.Nodes) != 2 ||
		second.Evidence.Nodes[0].Name != "NewBranch" {
		t.Fatal("follow-up reused stale metadata or required extra model requests")
	}
}

func TestSchemaChatAllowsAnswersWithoutMatchingTypes(t *testing.T) {
	requests := 0
	answer := "No relevant types were found in this snapshot; that does not establish source-system coverage."
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, chatModelResponse(fmt.Sprintf(`{"text":%q,"labels":[]}`, answer)))
	}))
	defer server.Close()
	reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(context.Background(), nil, "Do we have an absent type?")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || reply.Text != answer || len(reply.Evidence.Nodes) != 0 || len(reply.Evidence.Relationships) != 0 {
		t.Fatalf("answer without matching metadata was lost: %#v", reply)
	}
	if reply.Diagnostics == nil || reply.Diagnostics.Usage != nil {
		t.Fatal("missing usage must remain unreported rather than counted as zero")
	}
}

func TestSchemaChatUsesLongerDeadlineWithoutExtendingCallerDeadline(t *testing.T) {
	for _, test := range []struct {
		name                   string
		callerTimeout          time.Duration
		minSeconds, maxSeconds int
	}{
		{"chat deadline", 5 * time.Minute, 46, 120},
		{"shorter caller deadline", 5 * time.Second, 1, 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seconds, err := strconv.Atoi(r.Header.Get("X-Server-Timeout"))
				if err != nil || seconds < test.minSeconds || seconds > test.maxSeconds {
					t.Errorf("unexpected SDK server timeout: %q, want %d-%d seconds", r.Header.Get("X-Server-Timeout"), test.minSeconds, test.maxSeconds)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, chatModelResponse(`{"text":"The snapshot includes GitHubBranch.","labels":["GitHubBranch"]}`))
				requests++
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), test.callerTimeout)
			defer cancel()
			reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(ctx, nil, "branches?")
			if err != nil {
				t.Fatal(err)
			}
			if requests != 1 || reply.Diagnostics == nil || reply.Diagnostics.TimeoutSeconds != 120 {
				t.Fatal("chat deadline was not applied and reported consistently")
			}
		})
	}
}

func chatModelResponse(text string) string {
	return fmt.Sprintf(`{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":%q}]}}]}`, text)
}

func chatGenerator(t *testing.T, server *httptest.Server, snapshot schema.Snapshot) *VertexGenerator {
	t.Helper()
	httpClient := &http.Client{Transport: diagnosticTransport{base: server.Client().Transport}}
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: "test-project", Location: "eu",
		HTTPClient: httpClient, HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	generator := NewVertexGenerator("test-project", "eu", "test-model", "test-chat-model", &testSchemaProvider{snapshot: snapshot}, slog.Default())
	generator.client = client
	return generator
}

func TestSchemaChatRejectsUnusableResponsesWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name, response, outcome string
	}{
		{"unstructured", chatModelResponse("Invented answer"), "invalid_response"},
		{"truncated", `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":"Partial answer"}]}}]}`, "output_limit"},
		{"blocked prompt", `{"promptFeedback":{"blockReason":"SAFETY"}}`, "blocked"},
		{"blocked candidate", `{"candidates":[{"finishReason":"SAFETY"}]}`, "blocked"},
		{"no candidates", `{}`, "empty_response"},
		{"incomplete", `{"candidates":[{"finishReason":"OTHER","content":{"role":"model","parts":[{"text":"Partial answer"}]}}]}`, "incomplete"},
		{"discovery tool unavailable", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"search_schema","args":{"query":"branch"}}}]}}]}`, "unexpected_tool_call"},
		{"query tool unavailable", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"execute_cypher","args":{"query":"MATCH (n) RETURN n"}}}]}}]}`, "unexpected_tool_call"},
		{"unknown label", chatModelResponse(`{"text":"Invented answer","labels":["GitHubBranch","InventedLabel"]}`), "invalid_references"},
		{"missing references", chatModelResponse(`{"text":"Unreferenced answer"}`), "invalid_response"},
		{"null references", chatModelResponse(`{"text":"Unreferenced answer","labels":null}`), "invalid_response"},
		{"invented properties", chatModelResponse(`{"text":"Answer","labels":["GitHubBranch"],"properties":{"name":"private-value"}}`), "invalid_response"},
		{"trailing content", chatModelResponse(`{"text":"Answer","labels":[]} {}`), "invalid_response"},
		{"too many references", chatModelResponse(`{"text":"Answer","labels":[` + strings.Repeat(`"GitHubBranch",`, 24) + `"GitHubBranch"]}`), "invalid_references"},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, test.response)
			}))
			defer server.Close()
			reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(context.Background(), nil, "branches?")
			if err == nil || reply.Text != "" || len(reply.Evidence.Nodes) != 0 || requests != 1 {
				t.Fatalf("unsafe or partial response: reply=%#v error=%v requests=%d", reply, err, requests)
			}
			d := reply.Diagnostics
			if d == nil || d.Outcome != test.outcome || len(d.Attempts) != 1 {
				t.Fatalf("failure diagnostics lost: %#v", d)
			}
		})
	}
}

func TestSchemaChatFailsWithoutMetadataBeforeContactingVertex(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Error("metadata failure must not contact the model")
	}))
	defer server.Close()
	for _, provider := range []*testSchemaProvider{
		{err: schema.ErrUnavailable},
		{snapshot: schema.Snapshot{Text: "malformed schema"}},
		{snapshot: schema.Snapshot{Text: strings.Repeat("x", schema.MaxBytes+1)}},
	} {
		generator := chatGenerator(t, server, schema.Snapshot{})
		generator.schemas = provider
		reply, err := generator.Chat(context.Background(), nil, "branches?")
		if err == nil || reply.Text != "" || requests != 0 {
			t.Fatalf("metadata failure was hidden: reply=%#v error=%v requests=%d", reply, err, requests)
		}
		if provider.err != nil && !errors.Is(err, provider.err) {
			t.Fatalf("metadata error cause lost: %v", err)
		}
	}
}

func TestSchemaChatDoesNotExposeRawProviderFailures(t *testing.T) {
	for _, test := range []struct {
		code int
		kind string
	}{
		{400, "api_error"},
		{429, "rate_limited"},
		{503, "unavailable"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.code)
				fmt.Fprintf(w, `{"error":{"code":%d,"message":"private-provider-details"}}`, test.code)
			}))
			defer server.Close()
			reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(context.Background(), nil, "branches?")
			if err == nil || reply.Text != "" || strings.Contains(err.Error(), "private-provider-details") ||
				reply.Diagnostics == nil || reply.Diagnostics.Outcome != test.kind {
				t.Fatalf("provider failure hidden or exposed: reply=%#v error=%v", reply, err)
			}
		})
	}
}

func TestSchemaChatRetainsDiagnosticsOnProviderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Goog-Request-Id", "chat-request-123")
		w.WriteHeader(http.StatusGatewayTimeout)
		fmt.Fprint(w, `{"error":{"code":504,"status":"DEADLINE_EXCEEDED","message":"private-provider-details"}}`)
	}))
	defer server.Close()
	generator := chatGenerator(t, server, schema.Snapshot{Text: chatSchema, Hash: "schema-hash"})
	var logs bytes.Buffer
	generator.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	reply, err := generator.Chat(context.Background(), nil, "private-question")
	var failure *workspace.GenerationError
	if !errors.As(err, &failure) || failure.Kind != "upstream_timeout" {
		t.Fatalf("provider timeout was not classified: %v", err)
	}
	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatal(err)
	}
	d := reply.Diagnostics
	if d == nil || d.Outcome != "upstream_timeout" || d.HTTPStatus != 504 || d.APIStatus != "DEADLINE_EXCEEDED" ||
		d.ModelRequests != 1 || len(d.Attempts) != 1 || d.Attempts[0].RequestID != "chat-request-123" {
		t.Fatalf("timeout diagnostics lost: %s", encoded)
	}
	if reply.Text != "" || len(reply.Evidence.Nodes) != 0 {
		t.Fatal("failure returned a partial answer")
	}
	for _, private := range []string{"private-provider-details", "private-question", "UnrelatedCloudType"} {
		if strings.Contains(string(encoded), private) || strings.Contains(logs.String(), private) || strings.Contains(failure.Error(), private) {
			t.Fatalf("private content leaked into diagnostics: %s", private)
		}
	}

	if !strings.Contains(logs.String(), `"outcome":"upstream_timeout"`) {
		t.Fatal("failed chat did not emit structured diagnostics")
	}
}

func TestSchemaChatDistinguishesLocalTimeoutCancellationAndNetworkFailure(t *testing.T) {
	for _, test := range []struct {
		err  error
		kind string
	}{
		{context.DeadlineExceeded, "local_timeout"},
		{context.Canceled, "canceled"},
		{errors.New("private-network-details"), "schema_chat_failed"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
				Backend: genai.BackendVertexAI, Project: "test-project", Location: "eu",
				HTTPClient: &http.Client{Transport: diagnosticTransport{base: failingTransport{err: test.err}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			generator := NewVertexGenerator("test-project", "eu", "test-model", "test-chat-model",
				&testSchemaProvider{snapshot: schema.Snapshot{Text: chatSchema}}, slog.Default())
			generator.client = client
			reply, err := generator.Chat(context.Background(), nil, "branches?")
			var failure *workspace.GenerationError
			if !errors.As(err, &failure) || failure.Kind != test.kind || !errors.Is(err, test.err) {
				t.Fatalf("expected preserved %s error, got %v", test.kind, err)
			}
			if test.kind == "local_timeout" && !strings.Contains(err.Error(), "120-second deadline") {
				t.Fatal("local timeout message reports the wrong chat deadline")
			}
			d := reply.Diagnostics
			if d == nil || d.Outcome != test.kind || d.HTTPStatus != 0 || d.Usage != nil || len(d.Attempts) != 1 ||
				d.Attempts[0].ErrorType == "" || strings.Contains(err.Error(), "private-network-details") {
				t.Fatalf("incorrect local failure diagnostics: %#v", d)
			}
		})
	}
}

func TestSchemaChatIdentifiesUnusableAnswerText(t *testing.T) {
	for _, test := range []struct {
		text, outcome string
	}{
		{" ", "empty_answer"},
		{strings.Repeat("x", 16001), "answer_size_limit"},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, chatModelResponse(fmt.Sprintf(`{"text":%q,"labels":["GitHubBranch"]}`, test.text)))
				requests++
			}))
			defer server.Close()
			reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(context.Background(), nil, "branches?")
			d := reply.Diagnostics
			if err == nil || reply.Text != "" || len(reply.Evidence.Nodes) != 0 || d == nil || d.Outcome != test.outcome ||
				requests != 1 {
				t.Fatalf("unusable answer was not diagnosed without partial evidence: error=%v diagnostics=%#v", err, d)
			}
		})
	}
}
