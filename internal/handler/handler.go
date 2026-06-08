package handler

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Neo4jQuerier is the only interface the handler depends on.
type Neo4jQuerier interface {
	FindNamespaceByIngress(ctx context.Context, hostname string) (string, error)
}

// Handler handles all HTTP traffic for the service.
type Handler struct {
	neo4j Neo4jQuerier
	tmpl  *template.Template
}

// New returns an initialised Handler.
func New(querier Neo4jQuerier) *Handler {
	return &Handler{
		neo4j: querier,
		tmpl:  template.Must(template.New("page").Parse(pageTmpl)),
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
	Input     string
	Namespace string
	NotFound  bool
	Error     string
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

const pageTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>TittPåGraphDataTing</title>
  <style>
    *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }

    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      background: #f0f2f5;
      min-height: 100vh;
      display: flex;
      flex-direction: column;
      align-items: center;
      padding: 5rem 1.5rem 3rem;
      color: #1a1a2e;
    }

    h1 {
      font-size: 1.9rem;
      font-weight: 700;
      letter-spacing: -0.5px;
      margin-bottom: 0.4rem;
    }

    .subtitle {
      font-size: 0.9rem;
      color: #666;
      margin-bottom: 2.5rem;
    }

    form {
      display: flex;
      gap: 0.5rem;
      width: 100%;
      max-width: 560px;
    }

    input[type="text"] {
      flex: 1;
      padding: 0.65rem 0.9rem;
      border: 1.5px solid #ccc;
      border-radius: 7px;
      font-size: 0.95rem;
      background: #fff;
      outline: none;
      transition: border-color 0.15s;
      font-family: "SF Mono", "Consolas", "Menlo", monospace;
    }
    input[type="text"]:focus { border-color: #0067c5; }

    button {
      padding: 0.65rem 1.3rem;
      background: #0067c5;
      color: #fff;
      border: none;
      border-radius: 7px;
      font-size: 0.95rem;
      font-weight: 600;
      cursor: pointer;
      transition: background 0.15s;
      white-space: nowrap;
    }
    button:hover { background: #0057a8; }

    .result {
      margin-top: 2rem;
      width: 100%;
      max-width: 560px;
    }

    .card {
      padding: 1rem 1.25rem;
      border-radius: 9px;
      font-size: 0.95rem;
      line-height: 1.5;
    }
    .card-found   { background: #eaf6ee; border: 1.5px solid #86c99a; color: #1b4d2e; }
    .card-missing { background: #fffbea; border: 1.5px solid #f0c040; color: #5a4000; }
    .card-error   { background: #fdf0f0; border: 1.5px solid #e8a0a0; color: #6b1a1a; }

    .card-label {
      font-size: 0.72rem;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.06em;
      opacity: 0.6;
      margin-bottom: 0.3rem;
    }
    .card-value {
      font-size: 1.15rem;
      font-weight: 700;
      font-family: "SF Mono", "Consolas", "Menlo", monospace;
    }
  </style>
</head>
<body>
  <h1>TittP&aring;GraphDataTing</h1>
  <p class="subtitle">Find the Kubernetes namespace that owns an ingress</p>

  <form method="GET" action="/">
    <input
      type="text"
      name="ingress"
      placeholder="https://sikkerhet.nav.no"
      value="{{.Input}}"
      autocomplete="off"
      spellcheck="false"
    />
    <button type="submit">Look up</button>
  </form>

  {{if .Namespace}}
  <div class="result">
    <div class="card card-found">
      <div class="card-label">Namespace</div>
      <div class="card-value">{{.Namespace}}</div>
    </div>
  </div>
  {{else if .NotFound}}
  <div class="result">
    <div class="card card-missing">
      No namespace found for <strong>{{.Input}}</strong>.
    </div>
  </div>
  {{else if .Error}}
  <div class="result">
    <div class="card card-error">{{.Error}}</div>
  </div>
  {{end}}
</body>
</html>`
