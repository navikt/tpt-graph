package workspace

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/schema"
)

func TestInvestigationFailureTracesAndCorrelatesLogsDespiteHTTP200(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(traces)
	t.Cleanup(func() {
		otel.SetTracerProvider(old)
		_ = traces.Shutdown(context.Background())
	})
	var logs bytes.Buffer
	logger := slog.New(observability.NewTraceHandler(slog.NewJSONHandler(&logs, nil)))
	handler := NewHTTPHandler(NewService(
		testGenerator{err: context.DeadlineExceeded}, &testExecutor{}, &testCatalog{},
	), logger, nil)
	request := httptest.NewRequest(http.MethodPost, "/ask", strings.NewReader("question=Find+nodes"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	spans := recorder.Ended()
	if response.Code != http.StatusOK || len(spans) != 1 || spans[0].Status().Code != codes.Error {
		t.Fatalf("expected failed investigation trace without changing response: status=%d spans=%v", response.Code, spans)
	}
	if !strings.Contains(logs.String(), `"trace_id":"`+spans[0].SpanContext().TraceID().String()+`"`) {
		t.Fatalf("investigation error log is not correlated: %s", logs.String())
	}
}

func TestCatalogueFailureDoesNotHideResultsOrPretendCatalogueIsEmpty(t *testing.T) {
	for _, path := range []string{"/", "/ask"} {
		t.Run(path, func(t *testing.T) {
			handler := NewHTTPHandler(NewService(
				testGenerator{query: GeneratedQuery{Cypher: "RETURN 1"}},
				&testExecutor{result: QueryResult{Columns: []string{"value"}, Rows: [][]string{{"available-result"}}}},
				&testCatalog{listErr: errors.New("database unavailable")},
			), nil, nil)
			method := http.MethodGet
			if path == "/ask" {
				method = http.MethodPost
			}
			request := httptest.NewRequest(method, path, strings.NewReader("question=Find+nodes"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusInternalServerError || !strings.Contains(body, "Could not load saved queries.") {
				t.Fatalf("catalogue failure was not reported: %d %s", response.Code, body)
			}
			if strings.Contains(body, "No saved queries yet.") {
				t.Fatal("failed catalogue load was presented as an empty catalogue")
			}
			if path == "/ask" && (!strings.Contains(body, "available-result") || strings.Contains(body, "query execution failed")) {
				t.Fatal("catalogue failure hid or misrepresented successful query results")
			}
		})
	}
}

type testGenerator struct {
	query GeneratedQuery
	err   error
}

func (g testGenerator) Generate(context.Context, string) (GeneratedQuery, error) {
	return g.query, g.err
}

type testExecutor struct {
	query      string
	parameters map[string]any
	result     QueryResult
	err        error
}

func (e *testExecutor) Execute(ctx context.Context, query string, parameters map[string]any) (QueryResult, error) {
	e.query = query
	e.parameters = parameters
	if err := ctx.Err(); err != nil {
		return e.result, err
	}
	return e.result, e.err
}

type testCatalog struct {
	queries []SavedQuery
	query   SavedQuery
	listErr error
}

func (c *testCatalog) Save(_ context.Context, query SavedQuery) (SavedQuery, error) {
	query.ID = "query-1"
	c.queries = append(c.queries, query)
	return query, nil
}

func (c *testCatalog) Get(_ context.Context, id string) (SavedQuery, error) {
	if id != c.query.ID {
		return SavedQuery{}, ErrNotFound
	}
	return c.query, nil
}

func TestRunSavedUsesStoredQueryWithoutGenerating(t *testing.T) {
	stored := SavedQuery{
		ID:         "saved-1",
		Name:       "Dependencies",
		Category:   "Draft",
		Cypher:     "MATCH (d:Dependency {name: $name}) RETURN d.version AS version",
		Parameters: map[string]any{"name": "example"},
	}
	executor := &testExecutor{result: QueryResult{Columns: []string{"version"}, Rows: [][]string{{"1.2.3"}}}}
	service := NewService(
		testGenerator{err: context.Canceled},
		executor,
		&testCatalog{query: stored},
	)
	handler := NewHTTPHandler(service, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/queries/saved-1/run", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("want status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	if executor.query != stored.Cypher {
		t.Errorf("executed query = %q, want stored query %q", executor.query, stored.Cypher)
	}
	if executor.parameters["name"] != "example" {
		t.Errorf("executed parameters = %#v, want stored parameters", executor.parameters)
	}
	if !strings.Contains(response.Body.String(), "1.2.3") {
		t.Errorf("response does not show query results: %s", response.Body.String())
	}
}

func TestSaveDefaultsCategoryToDraft(t *testing.T) {
	catalog := &testCatalog{}
	handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
	request := httptest.NewRequest(
		http.MethodPost,
		"/save",
		strings.NewReader("name=My+query&cypher=RETURN+1&parameters=%7B%7D"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusSeeOther {
		t.Fatalf("want redirect status %d, got %d: %s", http.StatusSeeOther, response.Code, response.Body.String())
	}
	if len(catalog.queries) != 1 || catalog.queries[0].Category != "Draft" {
		t.Fatalf("saved queries = %#v, want one query in Draft", catalog.queries)
	}
	listRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), "My query") {
		t.Fatalf("saved query is not visible in catalogue: status %d, body %s", listResponse.Code, listResponse.Body.String())
	}
}

func (c *testCatalog) List(context.Context) ([]SavedQuery, error) {
	return c.queries, c.listErr
}

func TestAskDisplaysGeneratedQueryAndResults(t *testing.T) {
	executor := &testExecutor{
		result: QueryResult{
			Columns: []string{"repository", "dependency"},
			Rows:    [][]string{{"navikt/tpt-graph", "neo4j-driver"}},
		},
	}

	service := NewService(
		testGenerator{query: GeneratedQuery{
			Cypher:     "MATCH (repo:GitHubRepository) RETURN repo.name AS repository",
			Parameters: map[string]any{"name": "tpt-graph"},
		}},
		executor,
		&testCatalog{},
	)
	handler := NewHTTPHandler(service, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/ask", strings.NewReader("question=Find+repositories"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("want status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	for _, want := range []string{"MATCH (repo:GitHubRepository)", "navikt/tpt-graph", "neo4j-driver"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("response does not contain %q", want)
		}
	}
	if executor.query != "MATCH (repo:GitHubRepository) RETURN repo.name AS repository" {
		t.Errorf("executed query = %q", executor.query)
	}
	if executor.parameters["name"] != "tpt-graph" {
		t.Errorf("executed parameters = %#v", executor.parameters)
	}
}

func TestAskRejectsWriteQueryBeforeExecution(t *testing.T) {
	executor := &testExecutor{}
	service := NewService(
		testGenerator{query: GeneratedQuery{Cypher: "MATCH (n) CREATE (:Unexpected)"}},
		executor,
		&testCatalog{},
	)

	_, err := service.Ask(context.Background(), "create a node")

	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want read-only validation error, got %v", err)
	}

	if executor.query != "" {
		t.Fatalf("write query was executed: %q", executor.query)
	}
}

func TestAskReturnsGenerationFailureWithoutExecuting(t *testing.T) {
	executor := &testExecutor{}
	service := NewService(
		testGenerator{err: errors.New("Vertex AI unavailable")},
		executor,
		&testCatalog{},
	)

	_, err := service.Ask(context.Background(), "find dependencies")

	if err == nil || !strings.Contains(err.Error(), "Vertex AI unavailable") {
		t.Fatalf("want generation error, got %v", err)
	}
	if executor.query != "" {
		t.Fatalf("query executed after generation failure: %q", executor.query)
	}
}

func TestAskPropagatesCancellationToExecutor(t *testing.T) {
	executor := &testExecutor{}
	service := NewService(
		testGenerator{query: GeneratedQuery{Cypher: "MATCH (n) RETURN n"}},
		executor,
		&testCatalog{},
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := service.Ask(ctx, "find nodes")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation error, got %v", err)
	}
	if executor.query == "" {
		t.Fatal("executor was not called with the generated query")
	}
}

func TestSaveRejectsNullAndTrailingQueryParameters(t *testing.T) {
	for _, parameters := range []string{"null", "{} {}"} {
		t.Run(parameters, func(t *testing.T) {
			catalog := &testCatalog{}
			handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
			form := url.Values{
				"name":       {"My query"},
				"cypher":     {"RETURN 1"},
				"parameters": {parameters},
			}

			request := httptest.NewRequest(http.MethodPost, "/save", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("want status %d, got %d: %s", http.StatusBadRequest, response.Code, response.Body.String())
			}
			if len(catalog.queries) != 0 {
				t.Fatalf("invalid parameters were saved: %#v", catalog.queries)
			}
		})
	}
}

func TestAskShowsPartialResultsAlongsideExecutionError(t *testing.T) {
	executor := &testExecutor{
		result: QueryResult{
			Columns: []string{"name"},
			Rows:    [][]string{{"partial-result"}},
		},
		err: errors.New("query timed out"),
	}
	handler := NewHTTPHandler(NewService(
		testGenerator{query: GeneratedQuery{Cypher: "MATCH (n) RETURN n.name AS name"}},
		executor,
		&testCatalog{},
	), nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/ask", strings.NewReader("question=Find+records"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	for _, want := range []string{"query timed out", "partial-result"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("response does not show %q: %s", want, response.Body.String())
		}
	}
}

type testSchemaStatus struct {
	status schema.Status
	err    error
}

func (s testSchemaStatus) Status(context.Context) (schema.Status, error) { return s.status, s.err }

func TestHomeShowsSchemaFreshnessAndDisablesGenerationWithoutCache(t *testing.T) {
	for _, test := range []struct {
		name   string
		status schema.Status
		err    error
		want   []string
	}{
		{"cached", schema.Status{Available: true, CapturedAt: "2026-10-23 00:00:10 CEST", Age: "11h0m0s"}, nil,
			[]string{"2026-10-23 00:00:10 CEST", "11h0m0s", "midnight Europe/Oslo"}},
		{"stale after failure", schema.Status{Available: true, Stale: true, RefreshFailed: true}, nil,
			[]string{"Schema refresh failed", "cached schema is stale"}},
		{"empty", schema.Status{}, nil,
			[]string{"No cached schema", "disabled", "Saved queries can still run"}},
		{"database unavailable", schema.Status{}, errors.New("database unavailable"),
			[]string{"Could not load schema status", "disabled", "Saved queries"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewHTTPHandler(
				NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil,
				testSchemaStatus{status: test.status, err: test.err},
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d", response.Code)
			}
			for _, want := range test.want {
				if !strings.Contains(response.Body.String(), want) {
					t.Errorf("missing %q in response: %s", want, response.Body.String())
				}
			}

			if test.status.Available && strings.Contains(response.Body.String(), "disabled") {
				t.Fatal("available (even stale) schema must permit generation")
			}
		})
	}
}

func TestWorkspaceAccessAndCrossOriginProtection(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		path   string
		origin string
		status int
	}{
		{"get without application credentials", http.MethodGet, "/", "", http.StatusOK},
		{"same-origin post", http.MethodPost, "/save", "https://workspace.example", http.StatusSeeOther},
		{"cross-origin post", http.MethodPost, "/save", "https://other.example", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog := &testCatalog{}
			handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
			request := httptest.NewRequest(test.method, "https://workspace.example"+test.path,
				strings.NewReader("name=Example&cypher=RETURN+1&parameters=%7B%7D"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if test.status == http.StatusForbidden && len(catalog.queries) != 0 {
				t.Fatal("cross-origin request saved a query")
			}
		})
	}
}

func TestSaveAndRerunPreserveNumericParameters(t *testing.T) {
	catalog := &testCatalog{}
	executor := &testExecutor{}
	handler := NewHTTPHandler(NewService(testGenerator{}, executor, catalog), nil, nil)
	form := url.Values{
		"name": {"Numeric query"}, "cypher": {"RETURN $id LIMIT $limit"},
		"parameters": {`{"id":9007199254740993,"limit":10,"nested":[{"count":9223372036854775807,"score":0.25}]}`},
	}
	request := httptest.NewRequest(http.MethodPost, "/save", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther || len(catalog.queries) != 1 {
		t.Fatalf("save failed: %d %s", response.Code, response.Body.String())
	}
	catalog.query = catalog.queries[0]
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/queries/query-1/run", nil))
	want := map[string]any{
		"id": int64(9007199254740993), "limit": int64(10),
		"nested": []any{map[string]any{"count": int64(9223372036854775807), "score": float64(0.25)}},
	}
	if response.Code != http.StatusOK || !reflect.DeepEqual(executor.parameters, want) {
		t.Fatalf("rerun status = %d, parameters = %#v, want %#v", response.Code, executor.parameters, want)
	}
}

func TestResultPagesShowCompleteCatalogue(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		err  error
	}{
		{"ask", "/ask", nil},
		{"ask execution failure", "/ask", errors.New("query timed out")},
		{"rerun", "/queries/first/run", nil},
		{"rerun execution failure", "/queries/first/run", errors.New("query timed out")},
	} {
		t.Run(test.name, func(t *testing.T) {
			queries := []SavedQuery{
				{ID: "first", Name: "First saved query", Cypher: "RETURN 1"},
				{ID: "second", Name: "Second saved query", Cypher: "RETURN 2"},
			}
			handler := NewHTTPHandler(NewService(
				testGenerator{query: GeneratedQuery{Cypher: "RETURN 1"}},
				&testExecutor{err: test.err}, &testCatalog{queries: queries, query: queries[0]},
			), nil, nil)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader("question=Find+nodes"))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			for _, query := range queries {
				if !strings.Contains(response.Body.String(), query.Name) {
					t.Errorf("missing %q from result page: %s", query.Name, response.Body.String())
				}
			}

		})
	}
}
