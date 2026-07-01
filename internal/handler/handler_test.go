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

// --- mocks (function-field pattern) ---

type mockNeo4j struct {
	findNamespaceByIngressFn func(ctx context.Context, hostname string) (neo4j.IngressMatch, error)
	findNamespaceByPathFn    func(ctx context.Context, path string) ([]neo4j.IngressMatch, error)
	findDependencyUsagesFn   func(ctx context.Context, name, version, ecosystem string) ([]neo4j.DependencyUsage, error)
	findLastSyncFn           func(ctx context.Context) ([]neo4j.ModuleSync, error)
}

func (m *mockNeo4j) FindNamespaceByIngress(ctx context.Context, hostname string) (neo4j.IngressMatch, error) {
	if m.findNamespaceByIngressFn != nil {
		return m.findNamespaceByIngressFn(ctx, hostname)
	}
	return neo4j.IngressMatch{}, nil
}

func (m *mockNeo4j) FindNamespaceByPath(ctx context.Context, path string) ([]neo4j.IngressMatch, error) {
	if m.findNamespaceByPathFn != nil {
		return m.findNamespaceByPathFn(ctx, path)
	}
	return nil, nil
}

func (m *mockNeo4j) FindDependencyUsages(ctx context.Context, name, version, ecosystem string) ([]neo4j.DependencyUsage, error) {
	if m.findDependencyUsagesFn != nil {
		return m.findDependencyUsagesFn(ctx, name, version, ecosystem)
	}
	return nil, nil
}

func (m *mockNeo4j) FindLastSync(ctx context.Context) ([]neo4j.ModuleSync, error) {
	if m.findLastSyncFn != nil {
		return m.findLastSyncFn(ctx)
	}
	return nil, nil
}

type mockWhodis struct {
	lookupTeamFn func(ctx context.Context, teamSlug string) (*whodis.Team, error)
}

func (m *mockWhodis) LookupTeam(ctx context.Context, teamSlug string) (*whodis.Team, error) {
	if m.lookupTeamFn != nil {
		return m.lookupTeamFn(ctx, teamSlug)
	}
	return nil, nil
}

// --- routing ---

func TestRouting(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"home", "/", http.StatusOK},
		{"graph", "/graph", http.StatusOK},
		{"ingress", "/ingress", http.StatusOK},
		{"dependency", "/dependency", http.StatusOK},
		{"unknown path falls back to home", "/does-not-exist", http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(&mockNeo4j{}, &mockWhodis{})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("%s: want %d, got %d", tt.path, tt.wantStatus, rec.Code)
			}
		})
	}
}

// --- /ingress query parsing ---

func TestIngress(t *testing.T) {
	tests := []struct {
		name         string
		query        string
		neo4j        *mockNeo4j
		wantStatus   int
		wantBody     string
		wantNoDBCall bool
	}{
		{
			name:         "no query renders page without DB call",
			query:        "",
			neo4j:        &mockNeo4j{},
			wantStatus:   http.StatusOK,
			wantNoDBCall: true,
		},
		{
			name:  "valid hostname URL dispatches hostname query",
			query: "?q=https://foo.nav.no",
			neo4j: &mockNeo4j{
				findNamespaceByIngressFn: func(_ context.Context, hostname string) (neo4j.IngressMatch, error) {
					return neo4j.IngressMatch{Namespace: "appsec", Workloads: []string{"tpt-graph"}}, nil
				},
			},
			wantStatus: http.StatusOK,
			wantBody:   "tpt-graph",
		},
		{
			name:  "path-only input dispatches path query",
			query: "?q=/some/path",
			neo4j: &mockNeo4j{
				findNamespaceByPathFn: func(_ context.Context, path string) ([]neo4j.IngressMatch, error) {
					return []neo4j.IngressMatch{{Namespace: "appsec", Workloads: []string{"tpt-graph"}}}, nil
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:  "URL with path dispatches path query",
			query: "?q=https://foo.nav.no/my/path",
			neo4j: &mockNeo4j{
				findNamespaceByPathFn: func(_ context.Context, path string) ([]neo4j.IngressMatch, error) {
					return []neo4j.IngressMatch{{Namespace: "appsec", Workloads: []string{"tpt-graph"}}}, nil
				},
			},
			wantStatus: http.StatusOK,
		},
		{
			name:       "non-URL input renders validation error",
			query:      "?q=not-a-url",
			neo4j:      &mockNeo4j{},
			wantStatus: http.StatusOK,
			wantBody:   "Invalid input",
		},
		{
			name:       "URL with query params renders validation error",
			query:      "?q=https://foo.nav.no%3Fx%3D1",
			neo4j:      &mockNeo4j{},
			wantStatus: http.StatusOK,
			wantBody:   "query parameters",
		},
		{
			name:       "non-HTTP scheme renders validation error",
			query:      "?q=ftp://foo.nav.no",
			neo4j:      &mockNeo4j{},
			wantStatus: http.StatusOK,
			wantBody:   "http or https",
		},
		{
			name:  "hostname match with no resolved workload renders warning",
			query: "?q=https://foo.nav.no",
			neo4j: &mockNeo4j{
				findNamespaceByIngressFn: func(_ context.Context, hostname string) (neo4j.IngressMatch, error) {
					return neo4j.IngressMatch{Namespace: "appsec"}, nil
				},
			},
			wantStatus: http.StatusOK,
			wantBody:   "No Nais workload could be resolved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(tt.neo4j, &mockWhodis{})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ingress"+tt.query, nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("want status %d, got %d", tt.wantStatus, rec.Code)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("want body to contain %q", tt.wantBody)
			}
		})
	}
}

// --- /dependency ---

func TestDependency(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		neo4j      *mockNeo4j
		wantStatus int
		wantBody   string
	}{
		{
			name:       "no name renders page without DB call",
			query:      "",
			neo4j:      &mockNeo4j{},
			wantStatus: http.StatusOK,
		},
		{
			name:  "name param triggers DB call and renders results",
			query: "?name=log4j",
			neo4j: &mockNeo4j{
				findDependencyUsagesFn: func(_ context.Context, name, _, _ string) ([]neo4j.DependencyUsage, error) {
					return []neo4j.DependencyUsage{{Cluster: "prod-gcp", Namespace: "appsec", App: "my-app"}}, nil
				},
			},
			wantStatus: http.StatusOK,
			wantBody:   "my-app",
		},
		{
			name:       "name with no results renders not-found state",
			query:      "?name=nonexistent-pkg",
			neo4j:      &mockNeo4j{},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := New(tt.neo4j, &mockWhodis{})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dependency"+tt.query, nil))
			if rec.Code != tt.wantStatus {
				t.Errorf("want status %d, got %d", tt.wantStatus, rec.Code)
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("want body to contain %q", tt.wantBody)
			}
		})
	}
}
