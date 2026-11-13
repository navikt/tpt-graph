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
	"net/url"
	"strings"
	"time"

	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/schema"
)

//go:embed templates/*.html static/*
var webFiles embed.FS

type SchemaStatusProvider interface {
	Status(context.Context) (schema.Status, error)
}

const sidebarCookie = "query_workspace_sidebar"

type HTTPHandler struct {
	service   *Service
	logger    *slog.Logger
	template  *template.Template
	schemas   SchemaStatusProvider
	sessions  *sessionStore
	assistant SchemaAssistant
}

func NewHTTPHandler(service *Service, logger *slog.Logger, schemas SchemaStatusProvider, assistant SchemaAssistant) http.Handler {
	return newHTTPHandler(service, logger, schemas, assistant, newSessionStore(sessionMemoryCap, sessionIdleTTL, time.Now))
}

func newHTTPHandler(service *Service, logger *slog.Logger, schemas SchemaStatusProvider, assistant SchemaAssistant, sessions *sessionStore) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	handler := &HTTPHandler{
		service:   service,
		logger:    logger,
		template:  template.Must(template.New("workspace.html").ParseFS(webFiles, "templates/*.html")),
		schemas:   schemas,
		sessions:  sessions,
		assistant: assistant,
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
	mux.HandleFunc("GET /schema", h.schemaHome)
	mux.HandleFunc("POST /schema/chat", h.schemaChat)
	mux.HandleFunc("POST /schema/reset", h.schemaReset)
	mux.HandleFunc("POST /ask", h.ask)
	mux.HandleFunc("POST /run", h.runWritten)
	mux.HandleFunc("POST /mode", h.switchMode)
	mux.HandleFunc("POST /save", h.save)
	mux.HandleFunc("POST /sidebar", h.toggleSidebar)
	mux.HandleFunc("POST /queries/{id}/run", h.runSaved)
	mux.HandleFunc("POST /runs/{id}/open", h.openInCypher)
	return mux
}

type pageData struct {
	Mode             InputMode
	Question         string
	CypherInput      string
	ParamsInput      string
	LastInput        string
	LastParams       string
	Stale            bool
	RunID            string
	Investigation    *Investigation
	Run              *runView
	Error            string
	Schema           *schema.Status
	SchemaError      string
	CatalogError     string
	DiagnosticJSON   string
	DiagnosticsError string
	Search           string
	Catalogue        catalogueView
	CatalogueCount   int
	NoSearchMatch    bool
	Save             saveView
	SidebarOpen      bool
	SessionEnded     bool
}

type saveView struct {
	Status        string
	Name, Folder  string
	Error         string
	NameInvalid   bool
	FolderInvalid bool
}

const runUnavailable = "This run is no longer available. Run the query again before saving or opening it."

func (h *HTTPHandler) home(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	mode := session.ActiveMode
	if requested := InputMode(r.URL.Query().Get("mode")); requested == ModeAsk || requested == ModeCypher {
		mode = requested
		if mode != session.ActiveMode {
			h.sessions.update(session.ID, func(s *browserSession) { s.activeMode = mode })
		}
	}
	h.render(w, r, http.StatusOK, session, mode, saveView{}, "")
}

func (h *HTTPHandler) ask(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, session, ModeAsk, saveView{}, "The question form is invalid or too large.")
		return
	}
	question := r.FormValue("question")
	investigation, err := h.service.Ask(r.Context(), question)
	h.logRunError(r, "investigation failed", err)
	session = h.storeRun(session, ModeAsk, modeState{Draft: question}, investigation, err)
	h.render(w, r, http.StatusOK, session, ModeAsk, saveView{}, "")
}

func (h *HTTPHandler) runWritten(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, session, ModeCypher, saveView{}, "The Cypher form is invalid or too large.")
		return
	}
	cypher, parameters := r.FormValue("cypher"), r.FormValue("parameters")
	investigation, err := h.service.RunWritten(r.Context(), cypher, parameters)
	h.logRunError(r, "written Cypher run failed", err)
	session = h.storeRun(session, ModeCypher, modeState{Draft: cypher, Parameters: parameters}, investigation, err)
	h.render(w, r, http.StatusOK, session, ModeCypher, saveView{}, "")
}

func (h *HTTPHandler) runSaved(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	query, investigation, err := h.service.RunSaved(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		h.render(w, r, http.StatusNotFound, session, session.ActiveMode, saveView{}, "Saved query not found.")
		return
	}
	h.logRunError(r, "saved query execution failed", err)
	mode := query.Kind.mode()
	state := modeState{Draft: investigation.Question}
	if mode == ModeCypher {
		state.Draft = query.Cypher
		var encodeErr error
		if state.Parameters, encodeErr = editableParameters(query.Parameters); encodeErr != nil {
			h.logRunError(r, "encoding saved query parameters", encodeErr)
		}
	}
	session = h.storeRun(session, mode, state, investigation, err)
	h.render(w, r, http.StatusOK, session, mode, saveView{}, "")
}

// switchMode keeps the current tab's unsubmitted draft and shows the other tab. It never runs anything.
func (h *HTTPHandler) switchMode(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, session, session.ActiveMode, saveView{}, "The form is invalid or too large.")
		return
	}
	target := InputMode(r.FormValue("mode"))
	if target != ModeCypher {
		target = ModeAsk
	}
	from := InputMode(r.FormValue("from"))
	h.sessions.update(session.ID, func(s *browserSession) {
		state := s.modes[from]
		switch from {
		case ModeAsk:
			state.Draft = r.FormValue("question")
			s.modes[from] = state
		case ModeCypher:
			state.Draft, state.Parameters = r.FormValue("cypher"), r.FormValue("parameters")
			s.modes[from] = state
		}
		s.activeMode = target
	})
	http.Redirect(w, r, "/?mode="+string(target), http.StatusSeeOther)
}

// openInCypher copies a run's Cypher and parameters into an unexecuted Write Cypher draft.
func (h *HTTPHandler) openInCypher(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	run := session.Modes[ModeAsk].Run
	if run == nil || run.ID != r.PathValue("id") || run.Investigation.Query.Cypher == "" {
		h.render(w, r, http.StatusBadRequest, session, ModeAsk, saveView{}, runUnavailable)
		return
	}
	parameters, err := editableParameters(run.Investigation.Query.Parameters)
	if err != nil {
		h.logRunError(r, "encoding parameters for Write Cypher", err)
		h.render(w, r, http.StatusInternalServerError, session, ModeAsk, saveView{}, "Could not copy the query parameters.")
		return
	}
	h.sessions.update(session.ID, func(s *browserSession) {
		state := s.modes[ModeCypher]
		state.Draft, state.Parameters = run.Investigation.Query.Cypher, parameters
		s.modes[ModeCypher] = state
		s.activeMode = ModeCypher
	})
	http.Redirect(w, r, "/?mode=cypher", http.StatusSeeOther)
}

func (h *HTTPHandler) toggleSidebar(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	cookie := &http.Cookie{Name: sidebarCookie, Path: "/", Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	if r.FormValue("open") == "false" {
		cookie.Value, cookie.MaxAge = "closed", 365*24*60*60
	}
	http.SetCookie(w, cookie)
	mode := ModeAsk
	if InputMode(r.FormValue("mode")) == ModeCypher {
		mode = ModeCypher
	}
	http.Redirect(w, r, "/?mode="+string(mode), http.StatusSeeOther)
}

func (h *HTTPHandler) save(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, session, session.ActiveMode, saveView{}, "The saved query form is invalid or too large.")
		return
	}
	mode := InputMode(r.FormValue("mode"))
	if mode != ModeCypher {
		mode = ModeAsk
	}
	session = h.keepDraft(session, mode, r.Form)
	form := saveView{Status: "error", Name: r.FormValue("name"), Folder: r.FormValue("folder")}
	run := session.Modes[mode].Run
	if run == nil || run.ID != r.FormValue("run") {
		h.render(w, r, http.StatusBadRequest, session, mode, saveView{}, runUnavailable)
		return
	}
	query, err := h.service.Save(r.Context(), form.Name, form.Folder, run.Investigation)
	if err != nil {
		h.logRunError(r, "saving query", err)
		form.Error = err.Error()
		name := strings.TrimSpace(form.Name)
		form.NameInvalid = name == "" || len(name) > 200
		_, folderErr := normalizeFolderPath(form.Folder)
		form.FolderInvalid = folderErr != nil
		if name == "" {
			form.Error = "Name is required to save the query."
		}
		h.render(w, r, http.StatusBadRequest, session, mode, form, "")
		return
	}
	saved := *run
	saved.Saved = &query
	h.sessions.update(session.ID, func(s *browserSession) {
		if state := s.modes[mode]; state.Run != nil && state.Run.ID == run.ID {
			state.Run = &saved
			s.modes[mode] = state
		}
	})
	state := session.Modes[mode]
	state.Run = &saved
	session.Modes[mode] = state
	h.render(w, r, http.StatusOK, session, mode, saveView{}, "")
}

// keepDraft stores the composer text that the save form carries (with JavaScript), so saving the
// last run does not discard edits made since.
func (h *HTTPHandler) keepDraft(session sessionView, mode InputMode, form url.Values) sessionView {
	field := "question"
	if mode == ModeCypher {
		field = "cypher"
	}
	if _, ok := form[field]; !ok {
		return session
	}
	state := session.Modes[mode]
	state.Draft = form.Get(field)
	if mode == ModeCypher {
		state.Parameters = form.Get("parameters")
	}
	h.sessions.update(session.ID, func(s *browserSession) {
		stored := s.modes[mode]
		stored.Draft, stored.Parameters = state.Draft, state.Parameters
		s.modes[mode] = stored
	})
	session.Modes[mode] = state
	return session
}

// storeRun records a finished run as the mode's last run. The returned view includes it even if
// the session ended meanwhile; the run then simply has no ID and cannot be saved.
func (h *HTTPHandler) storeRun(session sessionView, mode InputMode, state modeState, investigation Investigation, runErr error) sessionView {
	snapshot := &runSnapshot{ID: rand.Text(), Investigation: investigation, Input: state.Draft, Parameters: state.Parameters}
	if runErr != nil {
		snapshot.Error = runErr.Error()
	}
	state.Run = snapshot
	if !h.sessions.update(session.ID, func(s *browserSession) {
		s.modes[mode] = state
		s.activeMode = mode
	}) {
		snapshot.ID = ""
	}
	session.Modes[mode] = state
	session.ActiveMode = mode
	return session
}

func (h *HTTPHandler) logRunError(r *http.Request, message string, err error) {
	if err == nil {
		return
	}
	observability.RecordError(r.Context(), err)

	sanitizedError := strings.ReplaceAll(err.Error(), "\n", "")
	sanitizedError = strings.ReplaceAll(sanitizedError, "\r", "")

	h.logger.ErrorContext(r.Context(), message, "error", sanitizedError)
}

// editableParameters formats parameters for the Write Cypher parameter field; none gives an empty field.
func editableParameters(parameters map[string]any) (string, error) {
	if len(parameters) == 0 {
		return "", nil
	}
	encoded, err := json.MarshalIndent(parameters, "", "  ")
	return string(encoded), err
}

func (h *HTTPHandler) render(w http.ResponseWriter, r *http.Request, status int, session sessionView, mode InputMode, form saveView, pageError string) {
	state := session.Modes[mode]
	data := pageData{
		Mode: mode, Error: pageError, Save: form, SessionEnded: session.Ended,
		Search: strings.TrimSpace(r.URL.Query().Get("q")),
	}
	if cookie, err := r.Cookie(sidebarCookie); err != nil || cookie.Value != "closed" {
		data.SidebarOpen = true
	}
	switch mode {
	case ModeAsk:
		data.Question = state.Draft
	case ModeCypher:
		data.CypherInput, data.ParamsInput = state.Draft, state.Parameters
	}
	if run := state.Run; run != nil {
		investigation := run.Investigation
		data.Investigation, data.RunID = &investigation, run.ID
		data.LastInput, data.LastParams = run.Input, run.Parameters
		data.Stale = state.Draft != run.Input || mode == ModeCypher && state.Parameters != run.Parameters
		var err error
		data.Run, err = investigationView(investigation, run.Error)
		if err != nil {
			h.logRunError(r, "preparing investigation display", err)
			data.Investigation, data.Run = nil, nil
			data.DiagnosticsError = "Could not display the last run."
			status = http.StatusInternalServerError
		}
		if run.Saved != nil {
			data.Save = saveView{Status: "done", Name: run.Saved.Name, Folder: run.Saved.FolderPath}
		}
	}
	if data.Investigation != nil && data.Investigation.Query.Diagnostics != nil {
		encoded, err := json.MarshalIndent(data.Investigation.Query.Diagnostics, "", "  ")
		if err != nil {
			h.logRunError(r, "encoding generation diagnostics", err)
			data.DiagnosticsError = "Could not display generation diagnostics."
			status = http.StatusInternalServerError
		} else {
			data.DiagnosticJSON = string(encoded)
		}
	}
	queries, err := h.service.List(r.Context())
	if err != nil {
		h.logRunError(r, "listing saved queries", err)
		data.CatalogError = "Could not load saved queries."
		if status == http.StatusOK {
			status = http.StatusInternalServerError
		}
	} else {
		data.CatalogueCount = len(queries)
		data.Catalogue = buildCatalogue(queries, data.Search)
		data.NoSearchMatch = data.Catalogue.Matches == 0 && len(queries) > 0
	}
	if h.schemas != nil {
		schemaStatus, err := h.schemas.Status(r.Context())
		if err != nil {
			h.logRunError(r, "loading schema status", err)
			data.SchemaError = "Could not load schema status."
		}
		data.Schema = &schemaStatus
	}
	canSave := data.Run != nil && data.Run.CanSave && data.RunID != "" && data.CatalogError == ""
	if data.Save.Status == "" && canSave {
		data.Save = saveView{Status: "form", Folder: defaultFolder}
	}
	if data.Save.Status == "form" || data.Save.Status == "error" {
		if !canSave {
			data.Save.Status = ""
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := h.template.Execute(w, data); err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "rendering workspace page", "error", err)
	}
}
