package handler

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tpt-graph/internal/neo4j"
	"tpt-graph/internal/whodis"
)

//go:embed layout.html home.html ingress.html dependency.html
var templateFiles embed.FS

// Neo4jQuerier is the only interface the handler depends on for graph queries.
type Neo4jQuerier interface {
	FindNamespaceByIngress(ctx context.Context, hostname string) (string, error)
	FindDependencyUsages(ctx context.Context, name, version, ecosystem string) ([]neo4j.DependencyUsage, error)
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
	case "/ingress":
		h.handleIngress(w, r)
	case "/dependency":
		h.handleDependency(w, r)
	default:
		h.handleHome(w, r)
	}
}

func (h *Handler) handleHome(w http.ResponseWriter, r *http.Request) {
	render(w, h.templates["home"], pageData{})
}

// handleIngress resolves a namespace (and team ownership) from an ingress URL.
func (h *Handler) handleIngress(w http.ResponseWriter, r *http.Request) {
	data := pageData{ActiveTab: "ingress"}
	input := strings.TrimSpace(r.URL.Query().Get("ingress"))
	data.IngressInput = input

	if input != "" {
		hostname, err := extractHostname(input)
		if err != nil {
			data.IngressError = err.Error()
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()

			ns, err := h.neo4j.FindNamespaceByIngress(ctx, hostname)
			if err != nil {
				slog.Error("neo4j query failed", "err", err)
				data.IngressError = "Database query failed — please try again later."
			} else if ns == "" {
				data.IngressNotFound = true
			} else {
				data.Namespace = ns

				team, err := h.whodis.LookupTeam(ctx, ns)
				if err != nil {
					slog.Warn("whodis lookup failed", "namespace", ns, "err", err)
					data.TeamUnavailable = true
				} else {
					data.Team = team
				}
			}
		}
	}

	render(w, h.templates["ingress"], data)
}

// handleDependency finds all apps that use a given dependency name/version/ecosystem.
func (h *Handler) handleDependency(w http.ResponseWriter, r *http.Request) {
	data := pageData{ActiveTab: "dependency"}
	data.DepName = strings.TrimSpace(r.URL.Query().Get("name"))
	data.DepVersion = strings.TrimSpace(r.URL.Query().Get("version"))
	data.DepEcosystem = strings.TrimSpace(r.URL.Query().Get("ecosystem"))

	if data.DepName != "" && data.DepVersion != "" && data.DepEcosystem != "" {
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

// render executes the named template set and writes the response.
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
		"ingress":    "ingress.html",
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

	// Ingress lookup
	IngressInput    string
	Namespace       string
	IngressNotFound bool
	IngressError    string
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

// extractHostname parses raw into a bare hostname (no scheme, port, or path).
// Returns an error if raw is not a valid http/https URL or contains a path beyond "/".
func extractHostname(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid URL — expected format: https://sikkerhet.nav.no")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("URL scheme must be http or https")
	}
	if u.Path != "" && u.Path != "/" {
		return "", fmt.Errorf("URL must not contain a path (max: https://example.nav.no/)")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("URL must not contain query parameters or fragments")
	}
	return u.Hostname(), nil
}
