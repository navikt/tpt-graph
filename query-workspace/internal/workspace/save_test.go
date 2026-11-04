package workspace

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func saveForm(t *testing.T, body string) url.Values {
	t.Helper()
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	values := make(url.Values)
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, inSave bool) {
		attrs := make(map[string]string)
		for _, attr := range node.Attr {
			attrs[attr.Key] = attr.Val
		}
		if node.Data == "form" {
			inSave = attrs["action"] == "/save"
		}
		if inSave && node.Type == html.ElementNode && node.Data == "input" {
			values.Set(attrs["name"], attrs["value"])
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, inSave)
		}
	}
	walk(root, false)
	return values
}

func submitForm(handler http.Handler, path string, values url.Values) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSaveFeedbackPreservesInvestigationWithoutRerunning(t *testing.T) {
	catalog := &testCatalog{queries: []SavedQuery{{ID: "existing", Name: "Existing", FolderPath: "Team > Languages"}}}
	executor := &testExecutor{
		result: QueryResult{Columns: []string{"repository"}, Rows: [][]string{{"navikt/erfaringsforum"}}},
		err:    errors.New("TransactionTimedOut"),
	}
	handler := newTestHandler(NewService(testGenerator{query: GeneratedQuery{
		Cypher: "RETURN $language AS language", Parameters: map[string]any{"language": "PHP"},
	}}, executor, catalog), nil, nil)
	response := submitForm(handler, "/ask", url.Values{"question": {"Which non-archived repositories use PHP?"}})
	form := saveForm(t, response.Body.String())
	form.Set("name", "   ")
	form.Set("folder", " Languages ")
	executor.query = ""
	response = submitForm(handler, "/save", form)
	body := response.Body.String()
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid save status = %d", response.Code)
	}
	for _, want := range []string{"Name is required to save the query.", `aria-invalid="true"`, `<option value="Team">`, `<option value="Team &gt; Languages">`, `value=" Languages "`, "Which non-archived repositories use PHP?", "navikt/erfaringsforum", "TransactionTimedOut", "1 partial rows"} {
		if !strings.Contains(body, want) {
			t.Errorf("invalid save lost %q", want)
		}
	}
	if len(catalog.queries) != 1 || executor.query != "" {
		t.Fatal("invalid save stored or executed a query")
	}
	form = saveForm(t, body)
	form.Set("name", " PHP repos ")
	response = submitForm(handler, "/save", form)
	body = response.Body.String()
	if response.Code != http.StatusOK || len(catalog.queries) != 2 {
		t.Fatalf("valid save failed: %d; stored %d queries", response.Code, len(catalog.queries))
	}
	for _, want := range []string{"✓ Saved", "PHP repos", "Languages", "navikt/erfaringsforum", "1 partial rows"} {
		if !strings.Contains(body, want) {
			t.Errorf("save confirmation lost %q", want)
		}
	}
	saved := catalog.queries[1]
	if saved.Name != "PHP repos" || saved.FolderPath != "Languages" || saved.Question != "Which non-archived repositories use PHP?" || executor.query != "" {
		t.Fatalf("save changed or reran the investigation: %#v, executed %q", saved, executor.query)
	}
}

func TestSaveRejectsRunThatIsNoLongerCurrent(t *testing.T) {
	catalog := &testCatalog{}
	handler := newTestHandler(NewService(testGenerator{query: GeneratedQuery{Cypher: "RETURN 1"}}, &testExecutor{}, catalog), nil, nil)
	response := submitForm(handler, "/ask", url.Values{"question": {"Find repositories"}})
	form := saveForm(t, response.Body.String())
	if form.Get("run") == "" {
		t.Fatal("save form does not identify the run")
	}
	submitForm(handler, "/ask", url.Values{"question": {"Find something else"}})
	form.Set("name", "Outdated")
	response = submitForm(handler, "/save", form)
	if response.Code != http.StatusBadRequest || len(catalog.queries) != 0 || !strings.Contains(response.Body.String(), "Run the query again before saving") {
		t.Fatal("save of a replaced run was not rejected explicitly")
	}
}

func TestSaveKeepsTheUnsentDraftButStoresTheRun(t *testing.T) {
	catalog := &testCatalog{}
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
	form := saveForm(t, submitForm(handler, "/run", url.Values{"cypher": {"RETURN 1"}}).Body.String())
	form.Set("name", "One")
	form.Set("cypher", "RETURN 2")
	form.Set("parameters", `{"x": 1}`)
	body := submitForm(handler, "/save", form).Body.String()
	if len(catalog.queries) != 1 || catalog.queries[0].Cypher != "RETURN 1" {
		t.Fatalf("save did not store the run snapshot: %#v", catalog.queries)
	}
	for _, want := range []string{">RETURN 2</textarea>", "{&#34;x&#34;: 1}</textarea>", `id="stale-notice" >`} {
		if !strings.Contains(body, want) {
			t.Fatalf("save discarded the unsent draft; missing %q", want)
		}
	}
	if !strings.Contains(getPage(handler, "/?mode=cypher").Body.String(), ">RETURN 2</textarea>") {
		t.Fatal("the unsent draft was not kept in the session")
	}
}
