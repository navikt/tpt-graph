package workspace

import (
	"context"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
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
		handler := NewHTTPHandler(NewService(timedGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil)
		response := submitForm(handler, "/ask", url.Values{"question": {"Find repositories"}})
		for _, want := range []string{"timed out after 60.00 s", "not reached", `class="diagnostics" open`, "Find repositories"} {
			if !strings.Contains(response.Body.String(), want) {
				t.Errorf("missing %q", want)
			}
		}
	})
}

func TestInvalidQuestionIsNotCleared(t *testing.T) {
	handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil)
	question := strings.Repeat("x", 4001)
	response := submitForm(handler, "/ask", url.Values{"question": {question}})
	if !strings.Contains(response.Body.String(), ">"+question+"</textarea>") {
		t.Fatal("validation cleared the question")
	}
}

func TestMissingSchemaStillAllowsSavedRunAndHidesSave(t *testing.T) {
	catalog := &testCatalog{query: SavedQuery{ID: "old", Name: "Legacy query", Cypher: "RETURN 1"}}
	catalog.queries = []SavedQuery{catalog.query}
	handler := NewHTTPHandler(NewService(testGenerator{err: errors.New("must not generate")}, &testExecutor{
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
	handler := NewHTTPHandler(NewService(testGenerator{query: GeneratedQuery{
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
	handler := NewHTTPHandler(NewService(testGenerator{query: GeneratedQuery{Cypher: "RETURN 1"}}, &testExecutor{
		result: QueryResult{Columns: []string{"name"}, Rows: [][]string{{"available-result"}}},
	}, &testCatalog{listErr: errors.New("catalogue unavailable")}), nil, nil)
	response := submitForm(handler, "/ask", url.Values{"question": {"Find repositories"}})
	body := response.Body.String()
	if response.Code != http.StatusInternalServerError || !strings.Contains(body, "available-result") || !strings.Contains(body, `href="/">Retry</a>`) || strings.Contains(body, `action="/save"`) {
		t.Fatal("catalogue failure hid evidence, omitted retry, or offered saving")
	}
}

func TestCatalogueSearchNoMatchIsNotAnEmptyCatalogue(t *testing.T) {
	handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{
		queries: []SavedQuery{{ID: "php", Name: "PHP repos", Category: "Languages"}},
	}), nil, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/?q=COBOL", nil))
	body := response.Body.String()
	if !strings.Contains(body, "No saved query matches") || strings.Contains(body, "No saved queries yet.") {
		t.Fatal("no search matches were confused with an empty catalogue")
	}
}
