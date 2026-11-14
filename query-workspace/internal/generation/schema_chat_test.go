package generation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
	"tpt-graph/query-workspace/internal/schema"
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
		if !strings.Contains(string(body), `"thinkingLevel":"HIGH"`) {
			t.Errorf("chat must prioritize reasoning quality: %s", body)
		}
		if !strings.Contains(string(body), `"maxOutputTokens":16384`) {
			t.Error("chat must reserve room for reasoning and the final answer")
		}
		if strings.Contains(string(body), "UnrelatedCloudType") || !strings.Contains(string(body), "earlier-question") {
			t.Errorf("full schema leaked or conversation was lost: %s", body)
		}
		if requests == 0 && !strings.Contains(string(body), `"mode":"ANY"`) {
			t.Error("initial answer must require metadata retrieval")
		}
		w.Header().Set("Content-Type", "application/json")
		switch requests {
		case 0:
			fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"search_schema","args":{"query":"branch"}},"thoughtSignature":"c2lnbmF0dXJl"}]}}]}`)
		case 1:
			if !strings.Contains(string(body), "functionResponse") || !strings.Contains(string(body), "GitHubBranch") || !strings.Contains(string(body), "c2lnbmF0dXJl") {
				t.Error("lookup result or model thought signature was lost")
			}
			fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"inspect_node","args":{"name":"GitHubBranch"}}}]}}]}`)
		default:
			fmt.Fprint(w, `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"The snapshot includes GitHubBranch and a metadata-inferred BRANCH relationship."}]}}]}`)
		}
		requests++
	}))
	defer server.Close()
	captured := time.Now().UTC()
	generator := chatGenerator(t, server, schema.Snapshot{Text: chatSchema, CapturedAt: captured})
	reply, err := generator.Chat(context.Background(), []schema.Turn{
		{Question: "earlier-question", Reply: schema.Reply{Text: "Earlier answer."}},
	}, "Do we have branch information?")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 || !strings.Contains(reply.Text, "GitHubBranch") || len(reply.Evidence.Nodes) != 2 ||
		len(reply.Evidence.Relationships) != 1 || !reply.CapturedAt.Equal(captured) {
		t.Fatalf("ungrounded or incomplete answer: requests=%d reply=%#v", requests, reply)
	}
}

func chatGenerator(t *testing.T, server *httptest.Server, snapshot schema.Snapshot) *VertexGenerator {
	t.Helper()
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend: genai.BackendVertexAI, Project: "test-project", Location: "eu",
		HTTPClient: server.Client(), HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
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
	}{
		{"ungrounded", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"text":"Invented answer"}]}}]}`, 1},
		{"truncated", `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":"Partial answer"}]}}]}`, 1},
		{"blocked", `{"promptFeedback":{"blockReason":"SAFETY"}}`, 1},
		{"runaway", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"search_schema","args":{"query":"branch"}}}]}}]}`, 7},
		{"query tool unavailable", `{"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"name":"execute_cypher","args":{"query":"MATCH (n) RETURN n"}}}]}}]}`, 6},
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"private-provider-details"}}`)
	}))
	defer server.Close()
	reply, err := chatGenerator(t, server, schema.Snapshot{Text: chatSchema}).Chat(context.Background(), nil, "branches?")
	if err == nil || reply.Text != "" || strings.Contains(err.Error(), "private-provider-details") {
		t.Fatalf("provider failure hidden or exposed: reply=%#v error=%v", reply, err)
	}
}
