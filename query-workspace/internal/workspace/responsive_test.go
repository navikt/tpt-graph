package workspace

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSavedQueryDoesNotExpandCatalogueIntoInvestigation(t *testing.T) {
	handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/workspace.css", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("stylesheet status = %d", response.Code)
	}
	css, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	stylesheet := strings.Join(strings.Fields(string(css)), "")
	for _, rule := range []string{
		".sidebar>*,#catalogue-groups,.catalogue-group,.catalogue-groupul,.catalogue-item{min-width:0;max-width:100%}",
		".catalogue-groupul{width:100%}",
		".catalogue-item{width:100%}",
	} {
		if !strings.Contains(stylesheet, rule) {
			t.Errorf("responsive stylesheet is missing %q", rule)
		}
	}
	if !strings.Contains(string(css), ".catalogue-item>form{flex:0 0 auto}") {
		t.Error("saved query Run button must retain its intrinsic width")
	}
}
