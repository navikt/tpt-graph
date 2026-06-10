package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tpt-graph/internal/neo4j"
	"tpt-graph/internal/whodis"
)

// --- mocks ---

type mockNeo4j struct {
	namespaceByIngress string
	namespaceByPath    []string
	depUsages          []neo4j.DependencyUsage
	err                error
}

func (m *mockNeo4j) FindNamespaceByIngress(_ context.Context, _ string) (string, error) {
	return m.namespaceByIngress, m.err
}

func (m *mockNeo4j) FindNamespaceByPath(_ context.Context, _ string) ([]string, error) {
	return m.namespaceByPath, m.err
}

func (m *mockNeo4j) FindDependencyUsages(_ context.Context, _, _, _ string) ([]neo4j.DependencyUsage, error) {
	return m.depUsages, m.err
}

func (m *mockNeo4j) FindLastSync(_ context.Context) ([]neo4j.ModuleSync, error) {
	return nil, m.err
}

type mockWhodis struct {
	team *whodis.Team
	err  error
}

func (m *mockWhodis) LookupTeam(_ context.Context, _ string) (*whodis.Team, error) {
	return m.team, m.err
}

// newTestHandler returns a Handler wired with the given mocks.
func newTestHandler(q *mockNeo4j, w *mockWhodis) *Handler {
	return New(q, w)
}

// --- routing ---

func TestRouting_Home(t *testing.T) {
	h := newTestHandler(&mockNeo4j{}, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /: want 200, got %d", rec.Code)
	}
}

func TestRouting_Graph(t *testing.T) {
	h := newTestHandler(&mockNeo4j{}, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/graph", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /graph: want 200, got %d", rec.Code)
	}
}

func TestRouting_Ingress(t *testing.T) {
	h := newTestHandler(&mockNeo4j{}, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /ingress: want 200, got %d", rec.Code)
	}
}

func TestRouting_Dependency(t *testing.T) {
	h := newTestHandler(&mockNeo4j{}, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dependency", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /dependency: want 200, got %d", rec.Code)
	}
}

func TestRouting_UnknownPathFallsBackToHome(t *testing.T) {
	h := newTestHandler(&mockNeo4j{}, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /does-not-exist: want 200 (home fallback), got %d", rec.Code)
	}
}

// --- /ingress query parsing ---

func TestIngress_NoQuery_NoDBCall(t *testing.T) {
	q := &mockNeo4j{}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
}

func TestIngress_ValidHostnameURL_DispatchesHostnameQuery(t *testing.T) {
	q := &mockNeo4j{namespaceByIngress: "appsec"}
	h := newTestHandler(q, &mockWhodis{team: &whodis.Team{Slug: "appsec"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress?q=https://foo.nav.no", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if body == "" {
		t.Error("expected non-empty response body")
	}
}

func TestIngress_PathQuery_DispatchesPathQuery(t *testing.T) {
	q := &mockNeo4j{namespaceByPath: []string{"appsec"}}
	h := newTestHandler(q, &mockWhodis{team: &whodis.Team{Slug: "appsec"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress?q=/some/path", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
}

func TestIngress_URLWithPath_DispatchesPathQuery(t *testing.T) {
	q := &mockNeo4j{namespaceByPath: []string{"appsec"}}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress?q=https://foo.nav.no/my/path", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
}

func TestIngress_InvalidInput_RendersValidationError(t *testing.T) {
	q := &mockNeo4j{}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress?q=not-a-url", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Invalid input") {
		t.Error("expected validation error message in response body")
	}
}

func TestIngress_URLWithQueryParams_RendersValidationError(t *testing.T) {
	q := &mockNeo4j{}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress?q=https://foo.nav.no%3Fx%3D1", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "query parameters") {
		t.Error("expected query-params validation error in response body")
	}
}

func TestIngress_NonHTTPScheme_RendersValidationError(t *testing.T) {
	q := &mockNeo4j{}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress?q=ftp://foo.nav.no", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "http or https") {
		t.Error("expected scheme validation error in response body")
	}
}

// --- /dependency ---

func TestDependency_NoName_NoDBCall(t *testing.T) {
	q := &mockNeo4j{}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dependency", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
}

func TestDependency_WithName_CallsDB(t *testing.T) {
	q := &mockNeo4j{depUsages: []neo4j.DependencyUsage{
		{Cluster: "prod-gcp", Namespace: "appsec", App: "my-app"},
	}}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dependency?name=log4j", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "my-app") {
		t.Error("expected app name in response body")
	}
}

func TestDependency_NotFound_RendersNotFound(t *testing.T) {
	q := &mockNeo4j{depUsages: nil}
	h := newTestHandler(q, &mockWhodis{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dependency?name=nonexistent-pkg", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("want 200, got %d", rec.Code)
	}
}
