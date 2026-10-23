package workspace

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/queryparams"
	"tpt-graph/query-workspace/internal/schema"
)

//go:embed templates/*.html static/*
var webFiles embed.FS

type SchemaStatusProvider interface {
	Status(context.Context) (schema.Status, error)
}

type HTTPHandler struct {
	service    *Service
	logger     *slog.Logger
	template   *template.Template
	schemas    SchemaStatusProvider
	contextKey []byte
}

func NewHTTPHandler(service *Service, logger *slog.Logger, schemas SchemaStatusProvider) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &HTTPHandler{
		service:    service,
		logger:     logger,
		template:   template.Must(template.New("workspace.html").ParseFS(webFiles, "templates/*.html")),
		schemas:    schemas,
		contextKey: []byte(rand.Text()),
	}
	return http.NewCrossOriginProtection().Handler(observability.HTTPHandler(handler.routes()))
}

func (h *HTTPHandler) routes() *http.ServeMux {
	staticFiles, err := fs.Sub(webFiles, "static")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(staticFiles)))
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
	Question         string
	Search           string
	Groups           []catalogueGroup
	Categories       []string
	CatalogueCount   int
	NoSearchMatch    bool
	SavedName        string
	Run              *runView
	Save             saveView
	RunContext       string
}

type catalogueGroup struct {
	Name   string
	Items  []catalogueItem
	Hidden bool
}

type catalogueItem struct {
	SavedQuery
	Hidden bool
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
	if askErr != nil {
		observability.RecordError(r.Context(), askErr)
		h.logger.ErrorContext(r.Context(), "investigation failed", "error", askErr)
		h.render(w, r, http.StatusOK, pageData{
			Investigation: &investigation,
			Error:         askErr.Error(),
		})
		return
	}
	h.render(w, r, http.StatusOK, pageData{
		Investigation: &investigation,
	})
}

func (h *HTTPHandler) save(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, pageData{Error: "The saved query form is invalid or too large."})
		return
	}
	data := pageData{
		Question: r.FormValue("question"),
		Save:     saveView{Status: "error", Name: r.FormValue("name"), Category: r.FormValue("category")},
	}
	cypher := r.FormValue("cypher")
	parameters, err := queryparams.Decode([]byte(r.FormValue("parameters")))
	if token := r.FormValue("context"); token != "" {
		state, contextErr := h.decodeContext(token)
		if contextErr != nil {
			observability.RecordError(r.Context(), contextErr)
			h.logger.ErrorContext(r.Context(), "restoring investigation for saving", "error", contextErr)
			h.render(w, r, http.StatusBadRequest, pageData{Question: data.Question, Error: "Could not restore the investigation. Run the query again before saving."})
			return
		}
		data.Investigation, data.Error = &state.Investigation, state.Error
		data.Question = state.Investigation.Question
		cypher, parameters, err = state.Investigation.Query.Cypher, state.Investigation.Query.Parameters, nil
	}
	if err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "decoding saved query parameters", "error", err)
		data.Error = err.Error()
		h.render(w, r, http.StatusBadRequest, data)
		return
	}
	if data.Investigation == nil {
		data.Investigation = &Investigation{Question: data.Question, Query: GeneratedQuery{Cypher: cypher, Parameters: parameters}}
	}
	query, err := h.service.Save(
		r.Context(),
		data.Save.Name,
		data.Save.Category,
		data.Question,
		cypher,
		parameters,
	)
	if err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "saving query", "error", err)
		data.Save.Error = err.Error()
		data.Save.NameInvalid = strings.TrimSpace(data.Save.Name) == "" || len(strings.TrimSpace(data.Save.Name)) > 200
		data.Save.CategoryInvalid = len(strings.TrimSpace(data.Save.Category)) > 100
		if strings.TrimSpace(data.Save.Name) == "" {
			data.Save.Error = "Name is required to save the query."
		}
		h.render(w, r, http.StatusBadRequest, data)
		return
	}
	data.Save = saveView{Status: "done", Name: query.Name, Category: query.Category}
	h.render(w, r, http.StatusOK, data)
}

func (h *HTTPHandler) runSaved(w http.ResponseWriter, r *http.Request) {
	query, investigation, err := h.service.RunSaved(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			h.render(w, r, http.StatusNotFound, pageData{Error: "Saved query not found."})
			return
		}
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "saved query execution failed", "error", err)
	}
	data := pageData{Investigation: &investigation, SavedName: query.Name}
	if err != nil {
		data.Error = err.Error()
	}
	h.render(w, r, http.StatusOK, data)
}

func (h *HTTPHandler) render(w http.ResponseWriter, r *http.Request, status int, data pageData) {
	if data.Investigation != nil {
		data.Question = data.Investigation.Question
		if data.Investigation.Query.Parameters == nil {
			data.Investigation.Query.Parameters = map[string]any{}
		}
		encoded, err := json.Marshal(data.Investigation.Query.Parameters)
		if err != nil {
			observability.RecordError(r.Context(), err)
			h.logger.ErrorContext(r.Context(), "encoding query parameters", "error", err)
			data.Error = "Could not display query parameters."
			data.Investigation = nil
			h.render(w, r, http.StatusInternalServerError, data)
			return
		}
		data.GeneratedJSON = string(encoded)
		data.Run, err = investigationView(*data.Investigation, data.Error, data.SavedName)
		if err != nil {
			observability.RecordError(r.Context(), err)
			h.logger.ErrorContext(r.Context(), "preparing investigation display", "error", err)
			data.Error = "Could not display query parameters."
			status = http.StatusInternalServerError
		}
	}
	data.Search = strings.TrimSpace(r.URL.Query().Get("q"))
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
		data.CatalogueCount = len(queries)
		grouped := make(map[string][]catalogueItem)
		matches := 0
		for _, query := range queries {
			matched := strings.Contains(strings.ToLower(query.Name+" "+query.Category+" "+query.Question), strings.ToLower(data.Search))
			if matched {
				matches++
			}
			grouped[query.Category] = append(grouped[query.Category], catalogueItem{SavedQuery: query, Hidden: !matched})
		}
		for category := range grouped {
			data.Categories = append(data.Categories, category)
		}
		sort.Strings(data.Categories)
		for _, category := range data.Categories {
			group := catalogueGroup{Name: category, Items: grouped[category], Hidden: true}
			for _, item := range group.Items {
				if !item.Hidden {
					group.Hidden = false
				}
			}
			data.Groups = append(data.Groups, group)
		}
		data.NoSearchMatch = matches == 0 && len(queries) > 0
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
	if data.Run != nil && data.Run.CanSave && data.Save.Status == "" {
		data.Save = saveView{Status: "form", Category: "Draft"}
	}
	if data.Run != nil && data.Run.CanSave && data.Save.Status != "done" && data.CatalogError == "" {
		token, err := h.encodeContext(data)
		if err != nil {
			observability.RecordError(r.Context(), err)
			h.logger.ErrorContext(r.Context(), "encoding investigation context", "error", err)
			data.Save.Status = "hidden"
			data.DiagnosticsError = "Could not prepare the investigation for saving."
			status = http.StatusInternalServerError
		} else {
			data.RunContext = token
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.template.Execute(w, data); err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "rendering workspace page", "error", err)
	}
}
