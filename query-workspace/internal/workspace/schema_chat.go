package workspace

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/schema"
)

type SchemaAssistant interface {
	Chat(context.Context, []schema.Turn, string) (schema.Reply, error)
}

type schemaPage struct {
	Chat         []schema.Turn
	Draft, Error string
	Schema       *schema.Status
	SchemaError  string
	SessionEnded bool
	Evidence     *schema.Evidence
	Graph        *graphView
}

func (h *HTTPHandler) schemaHome(w http.ResponseWriter, r *http.Request) {
	h.renderSchema(w, r, http.StatusOK, h.sessions.load(w, r), "", "")
}

func (h *HTTPHandler) schemaChat(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		h.renderSchema(w, r, http.StatusBadRequest, session, "", "The schema question form is invalid or too large.")
		return
	}
	question := strings.TrimSpace(r.FormValue("question"))
	if question == "" || len(question) > schema.MaxQuestionBytes {
		h.renderSchema(w, r, http.StatusBadRequest, session, question, "Enter a schema question of 4000 bytes or fewer.")
		return
	}
	if h.assistant == nil {
		h.renderSchema(w, r, http.StatusServiceUnavailable, session, question, "The schema assistant is not configured.")
		return
	}
	reply, err := h.assistant.Chat(r.Context(), session.Chat, question)
	if err != nil {
		h.logRunError(r, "schema chat failed", err)
		h.renderSchema(w, r, http.StatusBadGateway, session, question, err.Error())
		return
	}
	conflict := false
	stored := h.sessions.update(session.ID, func(s *browserSession) {
		if s.chatVersion != session.ChatVersion {
			conflict = true
		} else {
			chat := append([]schema.Turn(nil), s.chat...)
			chat = append(chat, schema.Turn{Question: question, Reply: reply})
			if len(chat) > schema.MaxChatTurns {
				chat = append([]schema.Turn(nil), chat[len(chat)-schema.MaxChatTurns:]...)
			}
			s.chat, s.chatVersion = chat, s.chatVersion+1
		}
		session.Chat = append([]schema.Turn(nil), s.chat...)
		session.ChatVersion = s.chatVersion
	})
	if !stored || conflict {
		h.renderSchema(w, r, http.StatusConflict, session, question,
			"The schema conversation changed or expired while answering. This answer was not added. Send the question again.")
		return
	}
	h.renderSchema(w, r, http.StatusOK, session, "", "")
}

func (h *HTTPHandler) schemaReset(w http.ResponseWriter, r *http.Request) {
	session := h.sessions.load(w, r)
	if !h.sessions.update(session.ID, func(s *browserSession) {
		s.chat = nil
		s.chatVersion++
	}) {
		h.renderSchema(w, r, http.StatusConflict, session, "", "The schema conversation has expired. Reload the page to start again.")
		return
	}
	http.Redirect(w, r, "/schema", http.StatusSeeOther)
}

func (h *HTTPHandler) renderSchema(w http.ResponseWriter, r *http.Request, status int, session sessionView, draft, message string) {
	data := schemaPage{Chat: session.Chat, Draft: draft, Error: message, SessionEnded: session.Ended}
	if h.schemas != nil {
		current, err := h.schemas.Status(r.Context())
		if err != nil {
			h.logRunError(r, "loading schema status", err)
			data.SchemaError = "Could not load schema status."
		}
		data.Schema = &current
	}
	if len(data.Chat) > 0 {
		evidence := data.Chat[len(data.Chat)-1].Reply.Evidence
		data.Evidence = &evidence
		data.Graph = schemaGraph(evidence)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := h.template.ExecuteTemplate(w, "schema.html", data); err != nil {
		observability.RecordError(r.Context(), err)
		h.logger.ErrorContext(r.Context(), "rendering schema chat", "error", err)
	}
}

func schemaGraph(evidence schema.Evidence) *graphView {
	graph := ResultGraph{}
	for _, node := range evidence.Nodes {
		properties := map[string]string{}
		for _, property := range node.Properties {
			properties[property.Name] = property.Type
		}
		graph.Nodes = append(graph.Nodes, GraphNode{ID: "schema:" + node.Name, Labels: []string{node.Name}, Properties: properties})
	}
	for n, rel := range evidence.Relationships {
		graph.Relationships = append(graph.Relationships, GraphRelationship{
			ID: fmt.Sprintf("schema-edge:%d", n), Type: rel.Type, StartID: "schema:" + rel.Source, EndID: "schema:" + rel.Target,
		})
	}
	view := newGraphView(graph)
	if view != nil {
		view.Schema = true
		for n := range view.Payload.Nodes {
			view.Payload.Nodes[n].Name = view.Payload.Nodes[n].Label
			view.Nodes[n].Name = ""
		}
	}
	return view
}
