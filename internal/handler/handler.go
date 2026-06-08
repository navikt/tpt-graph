package handler

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tpt-graph/internal/whodis"
)

//go:embed page.html
var pageTmpl string

// Neo4jQuerier is the only interface the handler depends on for graph queries.
type Neo4jQuerier interface {
	FindNamespaceByIngress(ctx context.Context, hostname string) (string, error)
}

// WhodisClient is the interface for fetching team ownership information.
type WhodisClient interface {
	LookupTeam(ctx context.Context, teamSlug string) (*whodis.Team, error)
}

// Handler handles all HTTP traffic for the service.
type Handler struct {
	neo4j  Neo4jQuerier
	whodis WhodisClient
	tmpl   *template.Template
}

// New returns an initialised Handler.
func New(querier Neo4jQuerier, whodis WhodisClient) *Handler {
	return &Handler{
		neo4j:  querier,
		whodis: whodis,
		tmpl:   template.Must(template.New("page").Parse(pageTmpl)),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data := pageData{}
	input := strings.TrimSpace(r.URL.Query().Get("ingress"))
	data.Input = input

	if input != "" {
		hostname, err := extractHostname(input)
		if err != nil {
			data.Error = err.Error()
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()

			ns, err := h.neo4j.FindNamespaceByIngress(ctx, hostname)
			if err != nil {
				slog.Error("neo4j query failed", "err", err)
				data.Error = "Database query failed — please try again later."
			} else if ns == "" {
				data.NotFound = true
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.Execute(w, data); err != nil {
		slog.Error("template render failed", "err", err)
	}
}

// pageData is the view model passed to the HTML template.
type pageData struct {
	Input           string
	Namespace       string
	NotFound        bool
	Error           string
	Team            *whodis.Team
	TeamUnavailable bool
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
