package handler

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tpt-graph/internal/neo4j"
	"tpt-graph/internal/whodis"
)

//go:embed layout.html home.html search.html dependency.html
var templateFiles embed.FS

// Neo4jQuerier is the only interface the handler depends on for graph queries.
type Neo4jQuerier interface {
	FindNamespaceByIngress(ctx context.Context, hostname string) (string, error)
	FindNamespaceByPath(ctx context.Context, path string) ([]string, error)
	FindDependencyUsages(ctx context.Context, name, version, ecosystem string) ([]neo4j.DependencyUsage, error)
	FindLastSync(ctx context.Context) ([]neo4j.ModuleSync, error)
}

// WhodisClient is the interface for fetching team ownership information.
type WhodisClient interface {
	LookupTeam(ctx context.Context, teamSlug string) (*whodis.Team, error)
}

// Handler handles all HTTP traffic for the service.
type Handler struct {
	neo4j     Neo4jQuerier
	whodis    WhodisClient
	templates map[string]*template.Template
}

// New returns an initialised Handler.
func New(querier Neo4jQuerier, whodis WhodisClient) *Handler {
	return &Handler{
		neo4j:     querier,
		whodis:    whodis,
		templates: mustParseTemplates(),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/search":
		h.handleSearch(w, r)
	case "/dependency":
		h.handleDependency(w, r)
	default:
		h.handleHome(w, r)
	}
}

func (h *Handler) handleHome(w http.ResponseWriter, r *http.Request) {
	data := pageData{}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	syncs, err := h.neo4j.FindLastSync(ctx)
	if err != nil {
		slog.Warn("failed to fetch last sync times", "err", err)
	}
	data.ModuleSyncs = syncs

	render(w, h.templates["home"], data)
}

// handleSearch dispatches to ingress-by-hostname or ingress-by-path based on
// what the user typed:
//   - Valid http/https URL with no path (or just "/")  → hostname query
//   - Valid http/https URL with a real path            → path query using that path
//   - Starts with "/"                                  → path query directly
//   - Anything else                                    → validation error
func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	data := pageData{ActiveTab: "search"}
	input := strings.TrimSpace(r.URL.Query().Get("q"))
	data.SearchInput = input

	if input != "" {
		if strings.HasPrefix(input, "/") {
			h.runPathSearch(r, &data, input)
		} else {
			h.runURLSearch(r, &data, input)
		}
	}

	render(w, h.templates["search"], data)
}

// runURLSearch parses a full URL and dispatches to hostname or path query.
func (h *Handler) runURLSearch(r *http.Request, data *pageData, raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		data.SearchError = "Invalid input — enter a URL (https://example.nav.no) or a path (/my/path)."
		return
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		data.SearchError = "URL scheme must be http or https."
		return
	}
	if u.RawQuery != "" || u.Fragment != "" {
		data.SearchError = "URL must not contain query parameters or fragments."
		return
	}

	path := u.Path
	if path == "" || path == "/" {
		// No meaningful path — use hostname query.
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		h.queryByHostname(ctx, data, u.Hostname())
	} else {
		// URL contains a real path — use path query.
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		h.queryByPath(ctx, data, path)
	}
}

// runPathSearch runs a path query for an input that already starts with "/".
func (h *Handler) runPathSearch(r *http.Request, data *pageData, path string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	h.queryByPath(ctx, data, path)
}

// queryByHostname looks up the namespace (and team) for a given hostname.
func (h *Handler) queryByHostname(ctx context.Context, data *pageData, hostname string) {
	data.SearchMode = "hostname"
	ns, err := h.neo4j.FindNamespaceByIngress(ctx, hostname)
	if err != nil {
		slog.Error("neo4j ingress query failed", "err", err)
		data.SearchError = "Database query failed — please try again later."
		return
	}
	if ns == "" {
		data.SearchNotFound = true
		return
	}
	data.Namespace = ns
	h.lookupTeam(ctx, data, ns)
}

// queryByPath looks up namespace(s) for a given path fragment.
func (h *Handler) queryByPath(ctx context.Context, data *pageData, path string) {
	data.SearchMode = "path"
	namespaces, err := h.neo4j.FindNamespaceByPath(ctx, path)
	if err != nil {
		slog.Error("neo4j path query failed", "err", err)
		data.SearchError = "Database query failed — please try again later."
		return
	}
	switch len(namespaces) {
	case 0:
		data.SearchNotFound = true
	case 1:
		data.Namespace = namespaces[0]
		h.lookupTeam(ctx, data, namespaces[0])
	default:
		data.PathMatchCount = len(namespaces)
	}
}

// lookupTeam fetches whodis team data for a namespace, setting TeamUnavailable on error.
func (h *Handler) lookupTeam(ctx context.Context, data *pageData, namespace string) {
	team, err := h.whodis.LookupTeam(ctx, namespace)
	if err != nil {
		slog.Warn("whodis lookup failed", "namespace", namespace, "err", err)
		data.TeamUnavailable = true
		return
	}
	data.Team = team
}

// handleDependency finds all apps that use a given dependency name/version/ecosystem.
func (h *Handler) handleDependency(w http.ResponseWriter, r *http.Request) {
	data := pageData{ActiveTab: "dependency"}
	data.DepName = strings.TrimSpace(r.URL.Query().Get("name"))
	data.DepVersion = strings.TrimSpace(r.URL.Query().Get("version"))
	data.DepEcosystem = strings.TrimSpace(r.URL.Query().Get("ecosystem"))

	if data.DepName != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		usages, err := h.neo4j.FindDependencyUsages(ctx, data.DepName, data.DepVersion, data.DepEcosystem)
		if err != nil {
			slog.Error("dependency query failed", "err", err)
			data.DepError = "Database query failed — please try again later."
		} else if len(usages) == 0 {
			data.DepNotFound = true
		} else {
			data.DepUsages = usages
		}
	}

	render(w, h.templates["dependency"], data)
}

// render executes the layout template and writes the response.
func render(w http.ResponseWriter, tmpl *template.Template, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		slog.Error("template render failed", "err", err)
	}
}

// mustParseTemplates builds a template set per page by pairing layout.html
// with each page-specific content template.
func mustParseTemplates() map[string]*template.Template {
	pairs := map[string]string{
		"home":       "home.html",
		"search":     "search.html",
		"dependency": "dependency.html",
	}
	result := make(map[string]*template.Template, len(pairs))
	for name, contentFile := range pairs {
		result[name] = template.Must(
			template.New("").ParseFS(templateFiles, "layout.html", contentFile),
		)
	}
	return result
}

// pageData is the unified view model passed to all templates.
type pageData struct {
	ActiveTab string

	// Home
	ModuleSyncs []neo4j.ModuleSync

	// Search (ingress ownership — hostname or path)
	SearchInput     string
	SearchMode      string // "hostname" or "path"
	SearchNotFound  bool
	SearchError     string
	Namespace       string
	PathMatchCount  int
	Team            *whodis.Team
	TeamUnavailable bool

	// Dependency lookup
	DepName      string
	DepVersion   string
	DepEcosystem string
	DepUsages    []neo4j.DependencyUsage
	DepNotFound  bool
	DepError     string
}
