package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"

	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/queryparams"
	"tpt-graph/query-workspace/internal/schema"
)

type SchemaStatusProvider interface {
	Status(context.Context) (schema.Status, error)
}

type HTTPHandler struct {
	service  *Service
	logger   *slog.Logger
	template *template.Template
	schemas  SchemaStatusProvider
}

func NewHTTPHandler(service *Service, logger *slog.Logger, schemas SchemaStatusProvider) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &HTTPHandler{
		service:  service,
		logger:   logger,
		template: template.Must(template.New("workspace").Parse(workspacePage)),
		schemas:  schemas,
	}
	return http.NewCrossOriginProtection().Handler(observability.HTTPHandler(handler.routes()))
}

func (h *HTTPHandler) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.home)
	mux.HandleFunc("POST /ask", h.ask)
	mux.HandleFunc("POST /save", h.save)
	mux.HandleFunc("POST /queries/{id}/run", h.runSaved)
	return mux
}

type pageData struct {
	Queries          []SavedQuery
	Investigation    *Investigation
	Error            string
	GeneratedJSON    string
	Schema           *schema.Status
	SchemaError      string
	CatalogError     string
	DiagnosticJSON   string
	DiagnosticsError string
}

func (h *HTTPHandler) home(w http.ResponseWriter, r *http.Request) {
	h.render(w, r, http.StatusOK, pageData{})
}

func (h *HTTPHandler) ask(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, pageData{Error: "The question form is invalid or too large."})
		return
	}
	question := r.FormValue("question")
	investigation, askErr := h.service.Ask(r.Context(), question)
	encoded, marshalErr := json.Marshal(investigation.Query.Parameters)
	if marshalErr != nil {
		observability.RecordError(r.Context(), marshalErr)
		h.logger.ErrorContext(r.Context(), "encoding generated parameters", "error", marshalErr)
		h.render(w, r, http.StatusInternalServerError, pageData{Error: "Could not prepare the generated query for saving."})
		return
	}
	if askErr != nil {
		observability.RecordError(r.Context(), askErr)
		h.logger.ErrorContext(r.Context(), "investigation failed", "error", askErr)
		h.render(w, r, http.StatusOK, pageData{
			Investigation: &investigation,
			GeneratedJSON: string(encoded),
			Error:         askErr.Error(),
		})
		return
	}
	h.render(w, r, http.StatusOK, pageData{
		Investigation: &investigation,
		GeneratedJSON: string(encoded),
	})
}

func (h *HTTPHandler) save(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, pageData{Error: "The saved query form is invalid or too large."})
		return
	}
	parameters, err := queryparams.Decode([]byte(r.FormValue("parameters")))
	if err != nil {
		observability.RecordError(r.Context(), err)
		h.render(w, r, http.StatusBadRequest, pageData{Error: err.Error()})
		return
	}
	query, err := h.service.Save(
		r.Context(),
		r.FormValue("name"),
		r.FormValue("category"),
		r.FormValue("cypher"),
		parameters,
	)
	if err != nil {
		observability.RecordError(r.Context(), err)
		h.render(w, r, http.StatusBadRequest, pageData{Error: err.Error()})
		return
	}
	http.Redirect(w, r, "/?saved="+query.ID, http.StatusSeeOther)
}

func (h *HTTPHandler) runSaved(w http.ResponseWriter, r *http.Request) {
	query, result, err := h.service.RunSaved(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			h.render(w, r, http.StatusNotFound, pageData{Error: "Saved query not found."})
			return
		}
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "saved query execution failed", "error", err)
		encoded, marshalErr := json.Marshal(query.Parameters)
		if marshalErr != nil {
			h.logger.ErrorContext(r.Context(), "encoding saved query parameters", "error", marshalErr)
			h.render(w, r, http.StatusInternalServerError, pageData{Error: "Could not display saved query parameters."})
			return
		}
		h.render(w, r, http.StatusOK, pageData{
			Investigation: &Investigation{
				Question: query.Name,
				Query:    GeneratedQuery{Cypher: query.Cypher, Parameters: query.Parameters},
				Result:   result,
			},
			GeneratedJSON: string(encoded),
			Error:         err.Error(),
		})
		return
	}
	encoded, err := json.Marshal(query.Parameters)
	if err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "encoding saved query parameters", "error", err)
		h.render(w, r, http.StatusInternalServerError, pageData{Error: "Could not display saved query parameters."})
		return
	}
	h.render(w, r, http.StatusOK, pageData{
		Investigation: &Investigation{
			Question: query.Name,
			Query:    GeneratedQuery{Cypher: query.Cypher, Parameters: query.Parameters},
			Result:   result,
		},
		GeneratedJSON: string(encoded),
	})
}

func (h *HTTPHandler) render(w http.ResponseWriter, r *http.Request, status int, data pageData) {
	if data.Investigation != nil && data.Investigation.Query.Diagnostics != nil {
		encoded, err := json.MarshalIndent(data.Investigation.Query.Diagnostics, "", "  ")
		if err != nil {
			observability.RecordError(r.Context(), err)
			h.logger.ErrorContext(r.Context(), "encoding generation diagnostics", "error", err)
			data.DiagnosticsError = "Could not display generation diagnostics."
			status = http.StatusInternalServerError
		} else {
			data.DiagnosticJSON = string(encoded)
		}
	}
	queries, err := h.service.List(r.Context())
	if err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "listing saved queries", "error", err)
		data.CatalogError = "Could not load saved queries."
		if status == http.StatusOK {
			status = http.StatusInternalServerError
		}
	} else {
		data.Queries = queries
	}
	if h.schemas != nil {
		schemaStatus, err := h.schemas.Status(r.Context())
		if err != nil {
			observability.RecordError(r.Context(), err)
			h.logger.ErrorContext(r.Context(), "loading schema status", "error", err)
			data.SchemaError = "Could not load schema status."
		}
		data.Schema = &schemaStatus
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.template.Execute(w, data); err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "rendering workspace page", "error", err)
	}
}

const workspacePage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Query workspace</title>
</head>
<body>
  <main>
    <h1>Private query workspace</h1>
    {{if .Error}}<p role="alert">{{.Error}}</p>{{end}}
    {{if .SchemaError}}<p role="alert">{{.SchemaError}}</p>{{end}}
    {{if .Schema}}
      <section aria-labelledby="schema-title">
        <h2 id="schema-title">Schema cache</h2>
        <p>Refresh scheduled daily at midnight Europe/Oslo.</p>
        {{if .Schema.Available}}
          <p>Schema captured {{.Schema.CapturedAt}} (age: {{.Schema.Age}}).</p>
          {{if .Schema.Stale}}<p role="status">The cached schema is stale; generation uses the last successful snapshot.</p>{{end}}
        {{else}}
          <p role="status">No cached schema is available. Generation is disabled until a snapshot is captured. Saved queries can still run.</p>
        {{end}}
        {{if .Schema.RefreshFailed}}<p role="alert">Schema refresh failed. The last successful snapshot, if any, has been retained.</p>{{end}}
      </section>
    {{end}}
    <form method="post" action="/ask">
      <label for="question">Investigation question</label>
      <textarea id="question" name="question" required maxlength="4000">{{if .Investigation}}{{.Investigation.Question}}{{end}}</textarea>
      <button type="submit" {{if .Schema}}{{if not .Schema.Available}}disabled{{end}}{{end}}>Generate and run</button>
    </form>
    {{if .DiagnosticsError}}<p role="alert">{{.DiagnosticsError}}</p>{{end}}
    {{if .DiagnosticJSON}}
      <section aria-labelledby="diagnostics-title">
        <h2 id="diagnostics-title">Generation diagnostics</h2>
        <p>Generation duration: {{.Investigation.GenerationDuration}}. Execution duration: {{.Investigation.ExecutionDuration}}.</p>
        <p>Timings are in milliseconds. Missing fields were not reported or were not reached; they are not zero measurements.</p>
        <p>A timeout does not prove the question is too complex. Time waiting for the first response includes upstream processing and network latency.</p>
        <details open>
          <summary>Request, response and network metadata</summary>
          <pre><code>{{.DiagnosticJSON}}</code></pre>
        </details>
      </section>
    {{end}}
    {{if and .Investigation .Investigation.Query.Cypher}}
      <section aria-labelledby="query-title">
        <h2 id="query-title">Generated Cypher</h2>
        <pre><code>{{.Investigation.Query.Cypher}}</code></pre>
        <h2>Parameters</h2>
        <pre><code>{{.GeneratedJSON}}</code></pre>
        <h2>Results</h2>
        {{if .Investigation.Result.Columns}}
          <table>
            <thead><tr>{{range .Investigation.Result.Columns}}<th scope="col">{{.}}</th>{{end}}</tr></thead>
            <tbody>{{range .Investigation.Result.Rows}}<tr>{{range .}}<td>{{.}}</td>{{end}}</tr>{{end}}</tbody>
          </table>
          {{if .Investigation.Result.Truncated}}<p role="status">Results were truncated by the configured output limit.</p>{{end}}
          <p>{{len .Investigation.Result.Rows}} row(s) returned.</p>
          {{if .Error}}<p role="status">These are partial results because query execution failed.</p>{{end}}
        {{else if .Error}}
          <p role="status">No results were available before query execution failed.</p>
        {{else}}
          <p>0 rows returned.</p>
        {{end}}
        <form method="post" action="/save">
          <input type="hidden" name="cypher" value="{{.Investigation.Query.Cypher}}">
          <input type="hidden" name="parameters" value="{{.GeneratedJSON}}">
          <label for="name">Saved query name</label>
          <input id="name" name="name" required maxlength="200">
          <label for="category">Category</label>
          <input id="category" name="category" value="Draft" maxlength="100">
          <button type="submit">Save query</button>
        </form>
      </section>
    {{end}}
    <section aria-labelledby="saved-title">
      <h2 id="saved-title">Saved queries</h2>
      {{if .CatalogError}}
        <p role="alert">{{.CatalogError}}</p>
      {{else if .Queries}}
        <ul>
          {{range .Queries}}
            <li>
              <strong>{{.Name}}</strong> <span>{{.Category}}</span>
              <form method="post" action="/queries/{{.ID}}/run">
                <button type="submit">Run saved query</button>
              </form>
            </li>
          {{end}}
        </ul>
      {{else}}
        <p>No saved queries yet.</p>
      {{end}}
    </section>
  </main>
</body>
</html>`
