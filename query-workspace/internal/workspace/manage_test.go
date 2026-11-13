package workspace

import (
	"errors"
	"html"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManageSavedQueryRenamesAndMovesWithoutChangingContents(t *testing.T) {
	original := SavedQuery{
		ID: "saved-1", Name: "Original", FolderPath: "Drafts", Kind: KindCypher,
		Cypher: "RETURN $value", Parameters: map[string]any{"value": int64(42)}, CreatedAt: time.Now(),
	}

	catalog := &testCatalog{queries: []SavedQuery{original}}
	executor := &testExecutor{result: QueryResult{Columns: []string{"value"}, Rows: [][]string{{"kept-result"}}}}
	handler := newTestHandler(NewService(testGenerator{}, executor, catalog), nil, nil)
	submitForm(handler, "/run", url.Values{"cypher": {"RETURN 1"}})
	executor.query = ""

	response := submitForm(handler, "/queries/saved-1/update", url.Values{
		"name": {" Renamed "}, "folder": {" Team › Investigations "},
		"cypher": {"RETURN 2"}, "parameters": {`{"draft":true}`},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", response.Code, response.Body.String())
	}
	want := original
	want.Name, want.FolderPath = "Renamed", "Team > Investigations"
	if !reflect.DeepEqual(catalog.queries, []SavedQuery{want}) {
		t.Fatalf("updated queries = %#v, want %#v", catalog.queries, want)
	}
	for _, text := range []string{"Saved query updated.", ">RETURN 2</textarea>", "kept-result", "Renamed", "Investigations"} {
		if !strings.Contains(response.Body.String(), text) {
			t.Errorf("update response lost %q", text)
		}
	}
	if executor.query != "" {
		t.Fatal("updating metadata executed a query")
	}
	if !strings.Contains(getPage(handler, "/?mode=cypher").Body.String(), ">RETURN 2</textarea>") {
		t.Fatal("update did not retain the unsent draft")
	}
}

func TestManageSavedQueryForm(t *testing.T) {
	catalog := &testCatalog{queries: []SavedQuery{
		{ID: "saved-1", Name: "Original", FolderPath: "Drafts", Cypher: "RETURN 1"},
		{ID: "saved-2", Name: "Other", FolderPath: "Team > Investigations"},
	}}
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
	body := getPage(handler, "/").Body.String()
	if !strings.Contains(body, `action="/queries/saved-1/edit"`) {
		t.Fatal("catalogue has no management action")
	}
	response := submitForm(handler, "/queries/saved-1/edit", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("edit status = %d", response.Code)
	}
	for _, text := range []string{
		`action="/queries/saved-1/update"`, `name="name" value="Original"`,
		`name="folder" value="Drafts"`, `<option value="Team &gt; Investigations">`,
		"Save changes", "Cancel", "Rename or move",
	} {
		if !strings.Contains(response.Body.String(), text) {
			t.Errorf("management form missing %q", text)
		}
	}
}

func TestManageSavedQueryUpdateErrors(t *testing.T) {
	for _, test := range []struct {
		name, id, queryName, folder, message string
		storeErr                             error
		status                               int
	}{
		{name: "empty name", id: "saved-1", queryName: " ", folder: "Team", status: http.StatusBadRequest, message: "query name is required"},
		{name: "long name", id: "saved-1", queryName: strings.Repeat("x", 201), folder: "Team", status: http.StatusBadRequest, message: "200 characters"},
		{name: "long folder", id: "saved-1", queryName: "Renamed", folder: strings.Repeat("x", 101), status: http.StatusBadRequest, message: "100 characters"},
		{name: "deep folder", id: "saved-1", queryName: "Renamed", folder: "a>b>c>d>e>f>g>h>i>j>k", status: http.StatusBadRequest, message: "10 levels"},
		{name: "missing", id: "missing", queryName: "Renamed", folder: "Team", status: http.StatusNotFound, message: "Saved query not found."},
		{name: "database failure", id: "saved-1", queryName: "Renamed", folder: "Team", storeErr: errors.New("private database failure"), status: http.StatusInternalServerError, message: "Could not update the saved query."},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := SavedQuery{ID: "saved-1", Name: "Original", FolderPath: "Drafts", Cypher: "RETURN 1"}
			catalog := &testCatalog{queries: []SavedQuery{original}, updateErr: test.storeErr}
			handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
			response := submitForm(handler, "/queries/"+test.id+"/update", url.Values{
				"name": {test.queryName}, "folder": {test.folder},
			})
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.message) {
				t.Fatalf("update status = %d, want %d with %q", response.Code, test.status, test.message)
			}
			if !reflect.DeepEqual(catalog.queries, []SavedQuery{original}) {
				t.Fatal("failed update changed the query")
			}
			if test.status != http.StatusNotFound && !strings.Contains(response.Body.String(), `name="folder" value="`+html.EscapeString(test.folder)+`"`) {
				t.Fatal("failed update discarded the submitted form")
			}
			if strings.Contains(response.Body.String(), "private database failure") {
				t.Fatal("database error was exposed in the page")
			}
		})
	}
}

func TestManageSavedQueryDeleteRequiresConfirmation(t *testing.T) {
	for _, test := range []struct {
		name, id, confirm, message string
		storeErr                   error
		status                     int
		deleted                    bool
	}{
		{name: "not confirmed", id: "saved-1", status: http.StatusBadRequest, message: "Confirm deletion"},
		{name: "confirmed", id: "saved-1", confirm: "yes", status: http.StatusOK, deleted: true, message: "Saved query deleted."},
		{name: "missing", id: "missing", confirm: "yes", status: http.StatusNotFound, message: "Saved query not found."},
		{name: "database failure", id: "saved-1", confirm: "yes", storeErr: errors.New("private database failure"), status: http.StatusInternalServerError, message: "Could not delete the saved query."},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := SavedQuery{ID: "saved-1", Name: "Original", FolderPath: "Team > Old", Kind: KindCypher, Cypher: "RETURN 1"}
			other := SavedQuery{ID: "saved-2", Name: "Other", FolderPath: "Drafts", Cypher: "RETURN 2"}
			catalog := &testCatalog{queries: []SavedQuery{original, other}, deleteErr: test.storeErr}
			executor := &testExecutor{result: QueryResult{Columns: []string{"value"}, Rows: [][]string{{"kept-result"}}}}
			handler := newTestHandler(NewService(testGenerator{}, executor, catalog), nil, nil)
			submitForm(handler, "/queries/saved-1/run", nil)
			executor.query = ""
			edit := submitForm(handler, "/queries/saved-1/edit", nil).Body.String()
			for _, text := range []string{`action="/queries/saved-1/delete"`, `name="confirm" value="yes" required`, "cannot be undone"} {
				if !strings.Contains(edit, text) {
					t.Errorf("delete confirmation missing %q", text)
				}
			}

			response := submitForm(handler, "/queries/"+test.id+"/delete", url.Values{
				"confirm": {test.confirm}, "cypher": {"RETURN 3"},
			})
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.message) {
				t.Fatalf("delete status = %d, want %d with %q", response.Code, test.status, test.message)
			}
			want := []SavedQuery{original, other}
			if test.deleted {
				want = []SavedQuery{other}
				body := getPage(handler, "/?mode=cypher").Body.String()
				if strings.Contains(body, `data-path="Team`) || strings.Contains(body, "/queries/saved-1/run") {
					t.Fatal("deleted query or empty folder is still in the catalogue")
				}
			}
			if !reflect.DeepEqual(catalog.queries, want) || executor.query != "" {
				t.Fatal("delete changed another query or executed Cypher")
			}
			for _, text := range []string{"kept-result", ">RETURN 3</textarea>"} {
				if !strings.Contains(response.Body.String(), text) {
					t.Errorf("delete lost %q", text)
				}
			}
		})
	}
}

func TestManageSavedQueryRefreshesSavedConfirmation(t *testing.T) {
	catalog := &testCatalog{}
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
	form := saveForm(t, submitForm(handler, "/run", url.Values{"cypher": {"RETURN 1"}}).Body.String())
	form.Set("name", "Original")
	submitForm(handler, "/save", form)
	response := submitForm(handler, "/queries/query-1/update", url.Values{"name": {"Renamed"}, "folder": {"Team"}})
	body := response.Body.String()
	if !strings.Contains(body, "✓ Saved") || strings.Contains(body, "Original") || !strings.Contains(body, "Renamed") {
		t.Fatal("rename left an outdated saved confirmation")
	}
	submitForm(handler, "/queries/query-1/delete", url.Values{"confirm": {"yes"}})
	body = getPage(handler, "/?mode=cypher").Body.String()
	if strings.Contains(body, "✓ Saved") || saveForm(t, body).Get("run") == "" {
		t.Fatal("deleted query is still marked saved or its run cannot be saved again")
	}
}

func TestManageSavedQueryKeepsTheSubmittingTabsMode(t *testing.T) {
	catalog := &testCatalog{queries: []SavedQuery{{ID: "saved-1", Name: "Original", Cypher: "RETURN 1"}}}
	handler := newTestHandler(NewService(testGenerator{}, &testExecutor{}, catalog), nil, nil)
	submitForm(handler, "/run", url.Values{"cypher": {"RETURN 1"}})
	getPage(handler, "/?mode=ask")
	response := submitForm(handler, "/queries/saved-1/edit", url.Values{
		"mode": {"cypher"}, "cypher": {"RETURN 2"}, "parameters": {`{"draft":true}`},
	})
	for _, text := range []string{">RETURN 2</textarea>", "{&#34;draft&#34;:true}</textarea>"} {
		if !strings.Contains(response.Body.String(), text) {
			t.Errorf("management action lost the submitting tab's draft: %q", text)
		}
	}
}
