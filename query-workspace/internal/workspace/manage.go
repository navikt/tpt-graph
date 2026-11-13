package workspace

import (
	"errors"
	"net/http"
	"strings"
)

type manageView struct {
	ID, Name, Folder string
	Notice           string
	Error            string
	NameInvalid      bool
	FolderInvalid    bool
}

func (h *HTTPHandler) editSaved(w http.ResponseWriter, r *http.Request) {
	session, ok := h.managementSession(w, r)
	if !ok {
		return
	}
	query, err := h.service.catalog.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		h.logRunError(r, "loading saved query for editing", err)
		status, message := savedQueryError(err, "Could not load the saved query.")
		h.render(w, r, status, session, session.ActiveMode, saveView{}, message)
		return
	}
	h.renderPage(w, r, http.StatusOK, session, session.ActiveMode, saveView{}, "", &manageView{
		ID: query.ID, Name: query.Name, Folder: query.FolderPath,
	})
}

func (h *HTTPHandler) updateSaved(w http.ResponseWriter, r *http.Request) {
	session, ok := h.managementSession(w, r)
	if !ok {
		return
	}
	form := &manageView{ID: r.PathValue("id"), Name: r.PostForm.Get("name"), Folder: r.PostForm.Get("folder")}
	_, err := h.service.Update(r.Context(), form.ID, form.Name, form.Folder)
	if err != nil {
		h.logRunError(r, "updating saved query", err)
		status, message := savedQueryError(err, "Could not update the saved query.")
		if status == http.StatusNotFound {
			h.render(w, r, status, session, session.ActiveMode, saveView{}, message)
			return
		}
		form.NameInvalid = validateQueryName(strings.TrimSpace(form.Name)) != nil
		_, folderErr := normalizeFolderPath(form.Folder)
		form.FolderInvalid = folderErr != nil
		if form.NameInvalid || form.FolderInvalid {
			status, message = http.StatusBadRequest, err.Error()
		}
		form.Error = message
		h.renderPage(w, r, status, session, session.ActiveMode, saveView{}, "", form)
		return
	}
	h.renderPage(w, r, http.StatusOK, session, session.ActiveMode, saveView{}, "", &manageView{Notice: "Saved query updated."})
}

func savedQueryError(err error, message string) (int, string) {
	if errors.Is(err, ErrNotFound) {
		return http.StatusNotFound, "Saved query not found."
	}
	return http.StatusInternalServerError, message
}

func (h *HTTPHandler) deleteSaved(w http.ResponseWriter, r *http.Request) {
	session, ok := h.managementSession(w, r)
	if !ok {
		return
	}
	if r.PostForm.Get("confirm") != "yes" {
		h.render(w, r, http.StatusBadRequest, session, session.ActiveMode, saveView{}, "Confirm deletion before deleting the saved query.")
		return
	}
	if err := h.service.catalog.Delete(r.Context(), r.PathValue("id")); err != nil {
		h.logRunError(r, "deleting saved query", err)
		status, message := savedQueryError(err, "Could not delete the saved query.")
		h.render(w, r, status, session, session.ActiveMode, saveView{}, message)
		return
	}
	h.renderPage(w, r, http.StatusOK, session, session.ActiveMode, saveView{}, "", &manageView{Notice: "Saved query deleted."})
}

func (h *HTTPHandler) managementSession(w http.ResponseWriter, r *http.Request) (sessionView, bool) {
	session := h.sessions.load(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	if err := r.ParseForm(); err != nil {
		h.render(w, r, http.StatusBadRequest, session, session.ActiveMode, saveView{}, "The saved query form is invalid or too large.")
		return session, false
	}
	if mode := InputMode(r.PostForm.Get("mode")); mode == ModeAsk || mode == ModeCypher {
		session.ActiveMode = mode
		h.sessions.update(session.ID, func(s *browserSession) { s.activeMode = mode })
	}
	return h.keepDraft(session, session.ActiveMode, r.PostForm), true
}
