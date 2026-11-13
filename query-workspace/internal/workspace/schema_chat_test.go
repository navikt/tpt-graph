package workspace

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"tpt-graph/query-workspace/internal/schema"
)

type testSchemaAssistant struct {
	histories [][]schema.Turn
	err       error
}

func (a *testSchemaAssistant) Chat(_ context.Context, history []schema.Turn, question string) (schema.Reply, error) {
	a.histories = append(a.histories, history)
	return schema.Reply{
		Text:       "Found branch metadata, not actual branch records. <script>alert('unsafe')</script>",
		CapturedAt: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC),
		Evidence: schema.Evidence{
			Nodes: []schema.NodeType{
				{Name: "GitHubBranch", Properties: []schema.Property{{Name: "name", Type: "String"}}},
				{Name: "GitHubRepository"},
			},
			Relationships: []schema.RelationshipType{{Source: "GitHubRepository", Type: "BRANCH", Target: "GitHubBranch"}},
		},
	}, a.err
}

func TestSchemaChatKeepsFollowUpsIsolatedAndNeverExecutesCypher(t *testing.T) {
	assistant := &testSchemaAssistant{}
	executor := &testExecutor{}
	shared := NewHTTPHandler(NewService(testGenerator{}, executor, &testCatalog{}), nil, nil, assistant)
	browserA := &browser{handler: shared, cookies: make(map[string]*http.Cookie)}
	browserB := &browser{handler: shared, cookies: make(map[string]*http.Cookie)}
	send := func(b *browser, path, question string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"question": {question}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	first := send(browserA, "/schema/chat", "Do we have branches?")
	if first.Code != http.StatusOK {
		t.Fatalf("schema chat unavailable: %d %s", first.Code, first.Body)
	}
	for _, text := range []string{"Found branch metadata", "GitHubBranch", "String", "metadata-inferred", "graph-data", "2026-11-12", "&lt;script&gt;"} {
		if !strings.Contains(first.Body.String(), text) {
			t.Errorf("missing %q in schema answer", text)
		}
	}
	if strings.Contains(first.Body.String(), "<script>alert") {
		t.Fatal("model content was rendered as HTML")
	}
	send(browserA, "/schema/chat", "How is it connected?")
	send(browserB, "/schema/chat", "What do we have?")
	if len(assistant.histories) != 3 || len(assistant.histories[0]) != 0 ||
		len(assistant.histories[1]) != 1 || assistant.histories[1][0].Question != "Do we have branches?" ||
		len(assistant.histories[2]) != 0 || executor.query != "" {
		t.Fatalf("history leaked, was lost, or query executed: %#v query=%s", assistant.histories, executor.query)
	}
	send(browserA, "/schema/reset", "")
	send(browserA, "/schema/chat", "Start over")
	if len(assistant.histories[3]) != 0 {
		t.Fatal("reset did not clear context")
	}
}

func TestSchemaChatBoundsHistoryAndPreservesItOnFailure(t *testing.T) {
	assistant := &testSchemaAssistant{}
	b := &browser{
		handler: NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil, assistant),
		cookies: make(map[string]*http.Cookie),
	}
	send := func(question string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/schema/chat", strings.NewReader(url.Values{"question": {question}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	if w := send(strings.Repeat("x", 4001)); w.Code != http.StatusBadRequest || len(assistant.histories) != 0 {
		t.Fatal("oversized input reached the model")
	}
	for range 10 {
		if w := send("branches?"); w.Code != http.StatusOK {
			t.Fatalf("chat failed: %d %s", w.Code, w.Body)
		}
	}
	if len(assistant.histories[9]) != 8 {
		t.Fatal("history is unbounded")
	}
	assistant.err = errors.New("Assistant is unavailable")
	w := send("keep this draft")
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "keep this draft") ||
		!strings.Contains(w.Body.String(), "Assistant is unavailable") {
		t.Fatal("assistant failure was hidden or lost the question")
	}
	if strings.Count(w.Body.String(), `<article class="schema-turn">`) != 8 {
		t.Fatal("failure changed the successful conversation")
	}
	foreign := httptest.NewRequest(http.MethodPost, "/schema/chat", strings.NewReader("question=foreign"))
	foreign.Header.Set("Origin", "https://foreign.example")
	foreign.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	b.ServeHTTP(w, foreign)
	if w.Code != http.StatusForbidden {
		t.Fatal("cross-origin chat request was accepted")
	}
}

type schemaAssistantFunc func(context.Context, []schema.Turn, string) (schema.Reply, error)

func (f schemaAssistantFunc) Chat(ctx context.Context, history []schema.Turn, question string) (schema.Reply, error) {
	return f(ctx, history, question)
}

func TestResetDuringSchemaChatDoesNotRestoreDiscardedConversation(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	assistant := schemaAssistantFunc(func(context.Context, []schema.Turn, string) (schema.Reply, error) {
		close(started)
		<-finish
		return schema.Reply{Text: "discarded-answer"}, nil
	})
	handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil, assistant)
	home := httptest.NewRecorder()
	handler.ServeHTTP(home, httptest.NewRequest(http.MethodGet, "/schema", nil))
	cookie := home.Result().Cookies()[0]
	chat := httptest.NewRequest(http.MethodPost, "/schema/chat", strings.NewReader("question=branches"))
	chat.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	chat.AddCookie(cookie)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, chat)
	}()
	<-started
	reset := httptest.NewRequest(http.MethodPost, "/schema/reset", nil)
	reset.AddCookie(cookie)
	resetResponse := httptest.NewRecorder()
	handler.ServeHTTP(resetResponse, reset)
	close(finish)
	<-done
	if resetResponse.Code != http.StatusSeeOther || response.Code != http.StatusConflict ||
		strings.Contains(response.Body.String(), "discarded-answer") {
		t.Fatalf("in-flight answer undid reset: reset=%d chat=%d %s", resetResponse.Code, response.Code, response.Body)
	}
}
