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

func TestSchemaChatExploresMetadataWithToolsWithoutFullSchemaPrompt(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/models/test-chat-model:generateContent") {
			t.Errorf("chat did not use its independent model: %s", r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		body, _ := json.Marshal(request)
		var declared struct {
			Tools []*genai.Tool
		}
		if err := json.Unmarshal(body, &declared); err != nil {
			t.Error(err)
		}
		if len(declared.Tools) != 1 || len(declared.Tools[0].FunctionDeclarations) != 2 ||
			declared.Tools[0].FunctionDeclarations[0].Name != "search_schema" ||
			declared.Tools[0].FunctionDeclarations[1].Name != "inspect_node" {
			t.Error("chat must expose only the two metadata tools")
		}
		if !strings.Contains(string(body), `"thinkingLevel":"MEDIUM"`) {
			t.Error("chat must use medium thinking on every model request")
		}
		if !strings.Contains(string(body), `"maxOutputTokens":16384`) {
			t.Error("chat must reserve room for reasoning and the final answer")
		}
		if strings.Contains(string(body), "UnrelatedCloudType") || strings.Contains(string(body), "private-history-diagnostics") ||
			!strings.Contains(string(body), "earlier-question") {
			t.Errorf("full schema leaked or conversation was lost: %s", body)
		}
		if requests == 0 && !strings.Contains(string(body), `"mode":"ANY"`) {
			t.Error("initial answer must require metadata retrieval")
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 0:
			fmt.Fprint(w, `{"modelVersion":"chat-version","responseId":"response-1","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15},"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"search_schema","args":{"query":"branch"}},"thoughtSignature":"c2lnbmF0dXJl"}]}}]}`)
		case 1:
			if !strings.Contains(string(body), "functionResponse") || !strings.Contains(string(body), "GitHubBranch") || !strings.Contains(string(body), "c2lnbmF0dXJl") {
				t.Error("lookup result or model thought signature was lost")
			}
			fmt.Fprint(w, `{"modelVersion":"chat-version","responseId":"response-2","usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":4,"thoughtsTokenCount":6,"totalTokenCount":30},"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"inspect_node","args":{"name":"GitHubBranch"}}}]}}]}`)
		default:
			fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"The snapshot includes GitHubBranch and a metadata-inferred BRANCH relationship."}]}}]}`)
		}
		requests++
	}))
	defer server.Close()
	captured := time.Now().UTC()
	generator := chatGenerator(t, server, schema.Snapshot{Text: chatSchema, CapturedAt: captured})
	reply, err := generator.Chat(context.Background(), []schema.Turn{
		{Question: "earlier-question", Reply: schema.Reply{Text: "Earlier answer.",
			Diagnostics: &modeldiag.Chat{Diagnostics: modeldiag.Diagnostics{TraceID: "private-history-diagnostics"}}}},
	}, "Do we have branch information?")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || !strings.Contains(reply.Text, "GitHubBranch") || len(reply.Evidence.Nodes) != 2 ||
		len(reply.Evidence.Relationships) != 1 || !reply.CapturedAt.Equal(captured) {
		t.Fatalf("ungrounded or incomplete answer: requests=%d reply=%#v", requests, reply)
	}
	d := reply.Diagnostics
	if d == nil || d.ThinkingLevel != "MEDIUM" || d.Outcome != "success" || d.LookupCount != 2 || d.HistoryTurns != 1 || len(d.Requests) != 3 ||
		len(d.Attempts) != 3 || d.DurationMS <= 0 || d.SchemaCaptured == nil || !d.SchemaCaptured.Equal(captured) {
		t.Fatalf("missing chat diagnostics: %#v", d)
	}
	if d.Usage == nil || d.Usage.TotalTokens != 45 || d.Usage.ThinkingTokens != 9 || d.UsageRequests != 2 ||
		d.Requests[0].Usage.TotalTokens != 15 || d.Requests[1].Usage.TotalTokens != 30 || d.Requests[2].Usage != nil {
		t.Fatalf("per-request usage lost or missing usage reported as zero: %#v", d)
	}
	for i, tool := range []string{"search_schema", "inspect_node"} {
		request := d.Requests[i]
		if request.Number != i+1 || request.HTTPStatus != 200 || request.ResponseID != fmt.Sprintf("response-%d", i+1) ||
			request.ModelVersion != "chat-version" || len(request.Lookups) != 1 || request.Lookups[0].Tool != tool ||
			request.Lookups[0].Outcome != "success" || request.Lookups[0].Nodes == 0 || request.DurationMS <= 0 {
			t.Fatalf("missing request or lookup diagnostics: %#v", request)
		}
	}
}

func TestSchemaChatAnswersAfterTwentyFourLookups(t *testing.T) {
	for _, batchSize := range []int{1, 8} {
		t.Run(fmt.Sprintf("batch_size_%d", batchSize), func(t *testing.T) {
			lookups, requests := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ToolConfig *genai.ToolConfig
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				requests++
				w.Header().Set("Content-Type", "application/json")
				if lookups == 24 {
					if request.ToolConfig == nil || request.ToolConfig.FunctionCallingConfig == nil ||
						request.ToolConfig.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeNone {
						t.Error("exhausted lookup budget must require a final answer without more tools")
					}
					fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"The snapshot includes GitHubBranch."}]}}]}`)
					return
				}
				if request.ToolConfig != nil && request.ToolConfig.FunctionCallingConfig != nil &&
					request.ToolConfig.FunctionCallingConfig.Mode == genai.FunctionCallingConfigModeNone {
					t.Error("metadata tools disabled before the lookup budget was exhausted")
				}
				parts := make([]string, batchSize)
				for i := range parts {
					parts[i] = `{"functionCall":{"name":"search_schema","args":{"query":"branch"}}}`
				}
				lookups += batchSize
				fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[%s]}}]}`, strings.Join(parts, ","))
			}))
			defer server.Close()

			reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(context.Background(), nil, "Explore branch metadata")
			if err != nil {
				t.Fatal(err)
			}
			if requests != 24/batchSize+1 || lookups != 24 || !strings.Contains(reply.Text, "GitHubBranch") || len(reply.Evidence.Nodes) == 0 {
				t.Fatalf("missing metadata-grounded answer: requests=%d lookups=%d reply=%#v", requests, lookups, reply)
			}
		})
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
				if requests == 0 {
					fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"search_schema","args":{"query":"branch"}}}]}}]}`)
				} else {
					fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"The snapshot includes GitHubBranch."}]}}]}`)
				}
				requests++
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), test.callerTimeout)
			defer cancel()
			reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(ctx, nil, "branches?")
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 || reply.Diagnostics == nil || reply.Diagnostics.TimeoutSeconds != 120 {
				t.Fatal("chat deadline was not applied and reported consistently")
			}
		})
	}
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

func TestSchemaChatRejectsUngroundedBlockedAndRunawayResponses(t *testing.T) {
	for _, test := range []struct {
		name, response string
		wantRequests   int
		outcome        string
		lookups        int
	}{
		{"ungrounded", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"Invented answer"}]}}]}`, 1, "ungrounded_answer", 0},
		{"truncated", `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":"Partial answer"}]}}]}`, 1, "output_limit", 0},
		{"blocked", `{"promptFeedback":{"blockReason":"SAFETY"}}`, 1, "blocked", 0},
		{"runaway", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"search_schema","args":{"query":"branch"}}}]}}]}`, 25, "lookup_budget", 24},
		{"query tool unavailable", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"execute_cypher","args":{"query":"MATCH (n) RETURN n"}}}]}}]}`, 24, "metadata_budget", 24},
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
			if err == nil || reply.Text != "" || len(reply.Evidence.Nodes) != 0 || requests != test.wantRequests {
				t.Fatalf("unsafe or partial response: reply=%#v error=%v requests=%d", reply, err, requests)
			}
			d := reply.Diagnostics
			if d == nil || d.Outcome != test.outcome || d.LookupCount != test.lookups || len(d.Requests) != requests {
				t.Fatalf("failure diagnostics lost: %#v", d)
			}
			if test.name == "query tool unavailable" && (len(d.Requests[0].Lookups) != 1 ||
				d.Requests[0].Lookups[0].Tool != "unknown" || d.Requests[0].Lookups[0].Outcome != "rejected") {
				t.Fatal("rejected tool lookup was not safely recorded")
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
		len(d.Requests) != 1 || d.Requests[0].HTTPStatus != 504 || len(d.Attempts) != 1 || d.Attempts[0].RequestID != "chat-request-123" {
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

func TestSchemaChatIdentifiesUnusableAnswerAfterSuccessfulLookup(t *testing.T) {
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
				if requests == 0 {
					fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"search_schema","args":{"query":"branch"}}}]}}]}`)
				} else {
					fmt.Fprintf(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":%q}]}}]}`, test.text)
				}
				requests++
			}))
			defer server.Close()
			reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(context.Background(), nil, "branches?")
			d := reply.Diagnostics
			if err == nil || reply.Text != "" || len(reply.Evidence.Nodes) != 0 || d == nil || d.Outcome != test.outcome ||
				d.LookupCount != 1 || len(d.Requests) != 2 || d.Requests[1].ToolMode != "AUTO" {
				t.Fatalf("unusable answer was not diagnosed without partial evidence: error=%v diagnostics=%#v", err, d)
			}
		})
	}
}
