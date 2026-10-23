package workspace

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCypherDisplayFormattingDoesNotChangeExecutedQuery(t *testing.T) {
	for _, test := range []struct {
		name, query, display string
	}{
		{"clauses and aliases",
			"MATCH (r:GitHubRepository) WHERE r.archived = false AND r.name = $name RETURN r.name AS repository, r.url AS url ORDER BY repository LIMIT 25",
			"MATCH (r:GitHubRepository)\nWHERE r.archived = false\nAND r.name = $name\nRETURN r.name AS repository,\n       r.url AS url\nORDER BY repository\nLIMIT 25"},
		{"quoted keywords",
			"RETURN 'MATCH WHERE AND RETURN', {name: 'LIMIT', values: [1, 2]} AS data",
			"RETURN 'MATCH WHERE AND RETURN',\n       {name: 'LIMIT', values: [1, 2]} AS data"},
		{"identifiers and comments",
			"MATCH (n) // RETURN WHERE\nWHERE n.return = $limit RETURN n.`ORDER BY` AS value",
			"MATCH (n) // RETURN WHERE\nWHERE n.return = $limit\nRETURN n.`ORDER BY` AS value"},
		{"optional match",
			"MATCH (n) OPTIONAL MATCH (n)-->(r) RETURN coalesce(r.name, 'none') AS name",
			"MATCH (n)\nOPTIONAL MATCH (n)-->(r)\nRETURN coalesce(r.name, 'none') AS name"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &testExecutor{}
			handler := NewHTTPHandler(NewService(testGenerator{query: GeneratedQuery{Cypher: test.query}}, executor, &testCatalog{}), nil, nil)
			form := url.Values{"question": {"Find repositories"}}
			request := httptest.NewRequest(http.MethodPost, "/ask", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if executor.query != test.query {
				t.Fatalf("executed query changed: %q", executor.query)
			}
			if !strings.Contains(response.Body.String(), `<code id="cypher">`+html.EscapeString(test.display)+`</code>`) {
				t.Fatalf("formatted query not displayed; want %q", test.display)
			}
		})
	}
}
