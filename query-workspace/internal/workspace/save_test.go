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
	catalog := &testCatalog{}
	executor := &testExecutor{
		result: QueryResult{Columns: []string{"repository"}, Rows: [][]string{{"navikt/erfaringsforum"}}},
		err:    errors.New("TransactionTimedOut"),
	}
	handler := NewHTTPHandler(NewService(testGenerator{query: GeneratedQuery{
		Cypher: "RETURN $language AS language", Parameters: map[string]any{"language": "PHP"},
	}}, executor, catalog), nil, nil)
	response := submitForm(handler, "/ask", url.Values{"question": {"Which non-archived repositories use PHP?"}})
	form := saveForm(t, response.Body.String())
	form.Set("name", "   ")
	form.Set("category", " Languages ")
	executor.query = ""
	response = submitForm(handler, "/save", form)
	body := response.Body.String()
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid save status = %d", response.Code)
	}
	for _, want := range []string{"Name is required to save the query.", `aria-invalid="true"`, `value=" Languages "`, "Which non-archived repositories use PHP?", "navikt/erfaringsforum", "TransactionTimedOut", "1 partial rows"} {
		if !strings.Contains(body, want) {
			t.Errorf("invalid save lost %q", want)
		}
	}
	if len(catalog.queries) != 0 || executor.query != "" {
		t.Fatal("invalid save stored or executed a query")
	}
	form = saveForm(t, body)
	form.Set("name", " PHP repos ")
	response = submitForm(handler, "/save", form)
	body = response.Body.String()
	if response.Code != http.StatusOK || len(catalog.queries) != 1 {
		t.Fatalf("valid save failed: %d; stored %d queries", response.Code, len(catalog.queries))
	}
	for _, want := range []string{"✓ Saved", "PHP repos", "Languages", "navikt/erfaringsforum", "1 partial rows"} {
		if !strings.Contains(body, want) {
			t.Errorf("save confirmation lost %q", want)
		}
	}
	saved := catalog.queries[0]
	if saved.Name != "PHP repos" || saved.Category != "Languages" || saved.Question != "Which non-archived repositories use PHP?" || executor.query != "" {
		t.Fatalf("save changed or reran the investigation: %#v, executed %q", saved, executor.query)
	}
}

func TestSaveRejectsTamperedInvestigationContext(t *testing.T) {
	catalog := &testCatalog{}
	handler := NewHTTPHandler(NewService(testGenerator{query: GeneratedQuery{Cypher: "RETURN 1"}}, &testExecutor{}, catalog), nil, nil)
	response := submitForm(handler, "/ask", url.Values{"question": {"Find repositories"}})
	form := saveForm(t, response.Body.String())
	context := form.Get("context")
	if context == "" {
		t.Fatal("save form has no protected investigation context")
	}
	form.Set("context", "x"+context)
	form.Set("name", "Tampered")
	response = submitForm(handler, "/save", form)
	if response.Code != http.StatusBadRequest || len(catalog.queries) != 0 || !strings.Contains(response.Body.String(), "Run the query again before saving") {
		t.Fatal("tampered context was not rejected explicitly")
	}
}
