package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"tpt-graph/internal/handler"
	"tpt-graph/internal/neo4j"
)

func main() {
	uri := mustEnv("NEO4J_URI")
	user := mustEnv("NEO4J_USER")
	password := mustEnv("NEO4J_PASSWORD")
	port := envOr("PORT", "8080")

	client, err := neo4j.NewClient(uri, user, password)
	if err != nil {
		slog.Error("failed to create neo4j client", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.VerifyConnectivity(ctx); err != nil {
		slog.Error("neo4j connectivity check failed", "err", err)
		os.Exit(1)
	}
	slog.Info("connected to neo4j", "uri", uri)

	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	mux := http.NewServeMux()
	mux.HandleFunc("/isready", ok)
	mux.HandleFunc("/isalive", ok)
	mux.Handle("/", handler.New(client))

	addr := fmt.Sprintf(":%s", port)
	slog.Info("server listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
