package graphapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Querier is the interface the handler depends on.
type Querier interface {
	GraphSeed(ctx context.Context, repo string) (*GraphPayload, error)
	GraphExpand(ctx context.Context, elementID string, knownIDs []string) (*GraphPayload, error)
}

// Handler serves the /api/graph/ endpoints.
type Handler struct {
	neo4j Querier
}

// NewHandler returns an initialised Handler.
func NewHandler(q Querier) *Handler {
	return &Handler{neo4j: q}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/graph/seed":
		h.handleSeed(w, r)
	case "/api/graph/expand":
		h.handleExpand(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleSeed(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		http.Error(w, `{"error":"repo parameter required"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	payload, err := h.neo4j.GraphSeed(ctx, repo)
	if err != nil {
		slog.Error("graph seed failed", "repo", repo, "err", err)
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	if payload == nil || len(payload.Nodes) == 0 {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	writeJSON(w, payload)
}

func (h *Handler) handleExpand(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, `{"error":"id parameter required"}`, http.StatusBadRequest)
		return
	}

	known := []string{}
	if k := r.URL.Query().Get("known"); k != "" {
		known = strings.Split(k, ",")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	payload, err := h.neo4j.GraphExpand(ctx, id, known)
	if err != nil {
		slog.Error("graph expand failed", "id", id, "err", err)
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	if payload == nil {
		payload = &GraphPayload{Nodes: []GraphNode{}, Edges: []GraphEdge{}}
	}

	writeJSON(w, payload)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("json encode failed", "err", err)
	}
}
