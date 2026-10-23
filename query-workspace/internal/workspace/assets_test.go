package workspace

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticRouteOnlyServesPublicAssets(t *testing.T) {
	handler := NewHTTPHandler(NewService(testGenerator{}, &testExecutor{}, &testCatalog{}), nil, nil)
	for _, test := range []struct {
		path string
		want int
	}{
		{"/static/plex-sans-400.ttf", http.StatusOK},
		{"/static/plex-sans-500.ttf", http.StatusOK},
		{"/static/plex-sans-600.ttf", http.StatusOK},
		{"/static/plex-mono-400.ttf", http.StatusOK},
		{"/static/plex-mono-500.ttf", http.StatusOK},
		{"/static/OFL-plex-sans.txt", http.StatusOK},
		{"/static/OFL-plex-mono.txt", http.StatusOK},
		{"/static/%2e%2e/templates/workspace.html", http.StatusNotFound},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.want {
			t.Errorf("%s: status = %d, want %d", test.path, response.Code, test.want)
		}
	}
}
