package workspace

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"tpt-graph/query-workspace/internal/schema"
)

type timedGenerator struct{}

func (timedGenerator) Generate(context.Context, string) (GeneratedQuery, error) {
	time.Sleep(time.Minute)
	return GeneratedQuery{}, context.DeadlineExceeded
}

func TestGenerationTimeoutShowsMeasuredTimeAndUnreachedSteps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		handler := newTestHandler(NewService(timedGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil)
		response := submitForm(handler, "/ask", url.Values{"question": {"Find repositories"}})
		for _, want := range []string{"timed out after 60.00 s", "not reached", `class="diagnostics" open`, "Find repositories"} {
			if !strings.Contains(response.Body.String(), want) {
				t.Errorf("missing %q", want)
			}
		}
	})
}

func TestInvalidQuestionIsNotCleared(t *testing.T) {
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil)
	question := strings.Repeat("x", 4001)
	response := submitForm(handler, "/ask", url.Values{"question": {question}})
	if !strings.Contains(response.Body.String(), ">"+question+"</textarea>") {
		t.Fatal("validation cleared the question")
	}
}

func TestMissingSchemaStillAllowsSavedRunAndHidesSave(t *testing.T) {
	catalog := &testCatalog{query: SavedQuery{ID: "old", Name: "Legacy query", Cypher: "RETURN 1"}}
	catalog.queries = []SavedQuery{catalog.query}
	handler := newTestHandler(NewService(testGenerator{err: errors.New("must not generate")}, &testExecutor{
		result: QueryResult{Columns: []string{"value"}, Rows: [][]string{{"saved-result"}}},
	}, catalog), nil, testSchemaStatus{status: schema.Status{}})
	response := submitForm(handler, "/queries/old/run", nil)
	for _, want := range []string{"saved-result", ">Legacy query</textarea>", `disabled aria-describedby="generation-note"`, `aria-label="Run saved query Legacy query"`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(response.Body.String(), `action="/save"`) {
		t.Fatal("rerun offered to save the same stored query")
	}
}

func TestInvestigationEscapesDataAndPreservesSignedIntegerParameters(t *testing.T) {
	catalog := &testCatalog{}
	injection := `<img src=x onerror=alert(1)>`
	handler := newTestHandler(NewService(testGenerator{query: GeneratedQuery{
		Cypher: "RETURN $value", Parameters: map[string]any{"value": int64(9223372036854775807)},
	}}, &testExecutor{result: QueryResult{Columns: []string{injection}, Rows: [][]string{{injection}}}}, catalog), nil, nil)
	response := submitForm(handler, "/ask", url.Values{"question": {injection}})
	body := response.Body.String()
	if strings.Contains(body, injection) || !strings.Contains(body, html.EscapeString(injection)) {
		t.Fatal("investigation content was not escaped")
	}
	form := saveForm(t, body)
	form.Set("name", injection)
	response = submitForm(handler, "/save", form)
	if response.Code != http.StatusOK || len(catalog.queries) != 1 || catalog.queries[0].Parameters["value"] != int64(9223372036854775807) {
		t.Fatal("signed context changed integer parameter precision")
	}
	if strings.Contains(response.Body.String(), injection) {
		t.Fatal("saved query name was not escaped")
	}
}

func TestCatalogueFailureHidesSaveButKeepsEvidence(t *testing.T) {
	handler := newTestHandler(NewService(testGenerator{query: GeneratedQuery{Cypher: "RETURN 1"}}, &testExecutor{
		result: QueryResult{Columns: []string{"name"}, Rows: [][]string{{"available-result"}}},
	}, &testCatalog{listErr: errors.New("catalogue unavailable")}), nil, nil)
	response := submitForm(handler, "/ask", url.Values{"question": {"Find repositories"}})
	body := response.Body.String()
	if response.Code != http.StatusInternalServerError || !strings.Contains(body, "available-result") || !strings.Contains(body, `href="/?mode=ask">Retry</a>`) || strings.Contains(body, `action="/save"`) {
		t.Fatal("catalogue failure hid evidence, omitted retry, or offered saving")
	}
}

func TestCatalogueSearchNoMatchIsNotAnEmptyCatalogue(t *testing.T) {
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{
		queries: []SavedQuery{{ID: "php", Name: "PHP repos", FolderPath: "Languages"}},
	}), nil, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/?q=COBOL", nil))
	body := response.Body.String()
	if !strings.Contains(body, "No saved query matches") || strings.Contains(body, "No saved queries yet.") {
		t.Fatal("no search matches were confused with an empty catalogue")
	}
}

func getPage(handler http.Handler, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	return response
}

func assertContains(t *testing.T, body string, want []string, unwanted []string) {
	t.Helper()
	for _, value := range want {
		if !strings.Contains(body, value) {
			t.Errorf("missing %q", value)
		}
	}
	for _, value := range unwanted {
		if strings.Contains(body, value) {
			t.Errorf("unexpected %q", value)
		}
	}
}

func TestWrittenCypherWithGraphResultsIsCompactAndDrawable(t *testing.T) {
	repo := GraphNode{ID: "4:repo", Labels: []string{"GitHubRepository"}, Properties: map[string]string{"name": "tpt-graph"}}
	language := GraphNode{ID: "4:go", Labels: []string{"ProgrammingLanguage"}, Properties: map[string]string{"name": "Go"}}
	executor := &testExecutor{result: QueryResult{
		Columns: []string{"r", "l"},
		Rows:    [][]string{{"(:GitHubRepository) tpt-graph", "(:ProgrammingLanguage) Go"}},
		Graph: ResultGraph{
			Nodes:         []GraphNode{repo, language},
			Relationships: []GraphRelationship{{ID: "5:uses", Type: "LANGUAGE", StartID: repo.ID, EndID: language.ID}},
			RowNodes:      [][]string{{repo.ID, language.ID}},
		},
	}}
	handler := newTestHandler(NewService(testGenerator{err: errors.New("must not generate")}, executor, &testCatalog{}), nil, nil)
	cypher := "MATCH (r:GitHubRepository)-[:LANGUAGE]->(l) RETURN r, l"
	response := submitForm(handler, "/run", url.Values{"cypher": {cypher}})
	if executor.query != cypher {
		t.Fatalf("executed %q", executor.query)
	}
	assertContains(t, response.Body.String(), []string{
		"Written Cypher", "1 row · 2 nodes · 1 relationship · ", `src="/static/cytoscape.min.js"`, `id="graph-data"`,
		`"source":"4:repo","target":"4:go"`, `data-nodes="4:repo 4:go"`, "Nodes in this result · 2", `aria-label="Selected node"`,
		`data-has-graph="true"`, ">" + html.EscapeString(cypher) + "</textarea>", `aria-pressed="true">Write Cypher`, "Save this query",
	}, []string{`class="steps"`, "What ran", "Graph view needs", "Open in Write Cypher"})
}

func TestWrittenCypherWithScalarResultsDisablesGraphView(t *testing.T) {
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{result: QueryResult{
		Columns: []string{"count"}, Rows: [][]string{{"42"}},
	}}, &testCatalog{}), nil, nil)
	response := submitForm(handler, "/run", url.Values{"cypher": {"MATCH (r:GitHubRepository) RETURN count(r) AS count"}})
	assertContains(t, response.Body.String(), []string{
		"Graph view needs nodes, relationships or paths in <code>RETURN</code>", `disabled title="Needs nodes, relationships or paths in RETURN"`, "42",
	}, []string{"cytoscape.min.js", `id="graph-data"`})
}

func TestWrittenCypherRejectedByValidationKeepsTheQuery(t *testing.T) {
	executor := &testExecutor{}
	handler := newTestHandler(NewService(testGenerator{}, executor, &testCatalog{}), nil, nil)
	response := submitForm(handler, "/run", url.Values{"cypher": {"MATCH (n) DELETE n"}})
	if executor.query != "" {
		t.Fatal("rejected query was executed")
	}
	assertContains(t, response.Body.String(), []string{
		"Validation rejected this query — nothing ran.", "validation: write clause DELETE is not permitted",
		"Your Cypher is kept above.", "Nothing executed", ">MATCH (n) DELETE n</textarea>",
	}, []string{`action="/save"`, `class="steps"`})
}

func TestWrittenCypherDoesNotNeedSchemaMetadata(t *testing.T) {
	executor := &testExecutor{}
	handler := newTestHandler(NewService(testGenerator{}, executor, &testCatalog{}), nil, testSchemaStatus{status: schema.Status{}})
	body := getPage(handler, "/?mode=cypher").Body.String()
	assertContains(t, body, []string{"No schema metadata available", `<button type="submit" class="primary">Validate and run</button>`, "Each tab keeps its own last result."},
		[]string{`disabled aria-describedby="generation-note"`})
	submitForm(handler, "/run", url.Values{"cypher": {"RETURN 1"}})
	if executor.query != "RETURN 1" {
		t.Fatal("written Cypher did not run without schema metadata")
	}
}

func TestEachTabKeepsItsOwnDraftAndResult(t *testing.T) {
	executor := &testExecutor{result: QueryResult{Columns: []string{"value"}, Rows: [][]string{{"generated-result"}}}}
	handler := newTestHandler(NewService(testGenerator{query: GeneratedQuery{Cypher: "RETURN 1 AS value"}}, executor, &testCatalog{}), nil, nil)
	submitForm(handler, "/ask", url.Values{"question": {"Find nodes"}})
	response := submitForm(handler, "/mode", url.Values{"mode": {"cypher"}, "from": {"ask"}, "question": {"Find other nodes"}})
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/?mode=cypher" {
		t.Fatalf("mode switch = %d %q", response.Code, response.Header().Get("Location"))
	}
	assertContains(t, getPage(handler, "/").Body.String(), []string{"Each tab keeps its own last result.", `name="cypher"`}, []string{"generated-result"})

	executor.result = QueryResult{Columns: []string{"value"}, Rows: [][]string{{"written-result"}}}
	submitForm(handler, "/run", url.Values{"cypher": {"RETURN 2 AS value"}, "parameters": {`{"a": 1}`}})
	submitForm(handler, "/mode", url.Values{"mode": {"ask"}, "from": {"cypher"}, "cypher": {"RETURN 3 AS value"}, "parameters": {`{"a": 1}`}})
	ask := getPage(handler, "/").Body.String()
	assertContains(t, ask, []string{"generated-result", ">Find other nodes</textarea>", `data-last-input="Find nodes"`}, []string{"written-result", `id="stale-notice" hidden`})

	cypher := getPage(handler, "/?mode=cypher").Body.String()
	assertContains(t, cypher, []string{"written-result", ">RETURN 3 AS value</textarea>", `data-last-input="RETURN 2 AS value"`, "<details class=\"parameters-field\" open>"},
		[]string{"generated-result", `id="stale-notice" hidden`})
}

func TestGeneratedRunShowsParameterValuesInlineAndOpensInWriteCypher(t *testing.T) {
	executor := &testExecutor{}
	cypher := "MATCH (r:GitHubRepository {name: $name}) RETURN r.name"
	handler := newTestHandler(NewService(testGenerator{query: GeneratedQuery{Cypher: cypher, Parameters: map[string]any{"name": `a"b`}}}, executor, &testCatalog{}), nil, nil)
	body := submitForm(handler, "/ask", url.Values{"question": {"Find repositories"}}).Body.String()
	assertContains(t, body, []string{
		`<mark title="$name = &#34;a\&#34;b&#34; (passed as a parameter)">&#34;a\&#34;b&#34;</mark>`,
		"1 parameter value shown inline, highlighted", `title="Copies a runnable version with values filled in"`,
		`{name: &#34;a\&#34;b&#34;}) RETURN r.name</pre>`,
	}, []string{">Parameters<"})
	if executor.parameters["name"] != `a"b` || executor.query != cypher {
		t.Fatal("inline display changed what was executed")
	}

	action := regexp.MustCompile(`action="(/runs/[^"]+/open)"`).FindStringSubmatch(body)
	if action == nil {
		t.Fatal("no Open in Write Cypher form")
	}
	executor.query = ""
	response := submitForm(handler, action[1], nil)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/?mode=cypher" {
		t.Fatalf("open = %d %q", response.Code, response.Header().Get("Location"))
	}
	assertContains(t, getPage(handler, "/?mode=cypher").Body.String(), []string{
		">" + html.EscapeString(cypher) + "</textarea>", html.EscapeString("{\n  \"name\": \"a\\\"b\"\n}") + "</textarea>",
		"Each tab keeps its own last result.",
	}, nil)
	if executor.query != "" {
		t.Fatal("opening in Write Cypher ran the query")
	}
	if response := submitForm(handler, "/runs/unknown/open", nil); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown run status = %d", response.Code)
	}
}

func TestSavedCypherRerunOpensWriteCypherTab(t *testing.T) {
	saved := SavedQuery{ID: "cy", Name: "Team repos", Kind: KindCypher, Cypher: "MATCH (t:GitHubTeam {slug: $team}) RETURN t", Parameters: map[string]any{"team": "appsec"}}
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{result: QueryResult{Columns: []string{"t"}, Rows: [][]string{{"x"}}}}, &testCatalog{query: saved, queries: []SavedQuery{saved}}), nil, nil)
	body := submitForm(handler, "/queries/cy/run", nil).Body.String()
	assertContains(t, body, []string{
		`Saved query &#34;Team repos&#34;`, `aria-pressed="true">Write Cypher`, ">" + html.EscapeString(saved.Cypher) + "</textarea>",
		html.EscapeString("{\n  \"team\": \"appsec\"\n}") + "</textarea>", `title="Saved Cypher">cy</span>`,
	}, []string{`class="steps"`, "What ran", `action="/save"`})
}

func TestDeepFolderTreeSearchOpensMatchingBranches(t *testing.T) {
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{queries: []SavedQuery{
		{ID: "1", Name: "Critical alerts", FolderPath: "Kari Nordmann > GitHub > Vuln search"},
		{ID: "2", Name: "Admins", FolderPath: "Kari Nordmann > GitHub"},
		{ID: "3", Name: "Languages", FolderPath: "Drafts"},
	}}), nil, nil)
	body := getPage(handler, "/?q=critical").Body.String()
	assertContains(t, body, []string{
		`data-path="Kari Nordmann &gt; GitHub &gt; Vuln search" style="--depth:2" >`,
		`data-path="Drafts" style="--depth:0" hidden`,
		`data-search="Admins Kari Nordmann &gt; GitHub " hidden`,
	}, nil)
}

func TestSidebarCollapseIsRenderedFromCookie(t *testing.T) {
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil)
	response := submitForm(handler, "/sidebar", url.Values{"open": {"false"}, "mode": {"cypher"}})
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/?mode=cypher" {
		t.Fatalf("sidebar toggle = %d %q", response.Code, response.Header().Get("Location"))
	}
	assertContains(t, getPage(handler, "/").Body.String(), []string{`class="workspace sidebar-closed"`, `aria-label="Show saved queries"`}, nil)
	submitForm(handler, "/sidebar", url.Values{"open": {"true"}})
	assertContains(t, getPage(handler, "/").Body.String(), []string{`class="workspace"`}, []string{"sidebar-closed\""})
}

func TestSessionsAreIsolatedAndAnEndedSessionIsReported(t *testing.T) {
	service := NewService(testGenerator{query: GeneratedQuery{Cypher: "RETURN 1"}}, &testExecutor{result: QueryResult{Columns: []string{"v"}, Rows: [][]string{{"private-result"}}}}, &testCatalog{})
	shared := NewHTTPHandler(service, nil, nil, nil)
	first, second := &browser{handler: shared, cookies: map[string]*http.Cookie{}}, &browser{handler: shared, cookies: map[string]*http.Cookie{}}
	submitForm(first, "/ask", url.Values{"question": {"My question"}})
	assertContains(t, getPage(second, "/").Body.String(), nil, []string{"private-result", "My question"})
	assertContains(t, getPage(first, "/").Body.String(), []string{"private-result", "My question"}, []string{"session has ended"})

	ended := &browser{handler: shared, cookies: map[string]*http.Cookie{sessionCookie: {Name: sessionCookie, Value: "forgotten"}}}
	assertContains(t, getPage(ended, "/").Body.String(), []string{"Your earlier workspace session has ended.", "Nothing was rerun."}, []string{"private-result"})
}
