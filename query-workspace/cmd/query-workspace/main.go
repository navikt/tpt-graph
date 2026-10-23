package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"tpt-graph/query-workspace/internal/catalog"
	"tpt-graph/query-workspace/internal/config"
	"tpt-graph/query-workspace/internal/generation"
	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/queryengine"
	"tpt-graph/query-workspace/internal/schema"
	"tpt-graph/query-workspace/internal/workspace"
)

func main() {
	logger := slog.New(observability.NewTraceHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		logger.Error("failed to load schema refresh timezone", "error", err)
		os.Exit(1)
	}
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startupCancel()

	store, err := catalog.NewPostgres(startupCtx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to initialize PostgreSQL catalogue", "error", err)
		os.Exit(1)
	}

	executor, err := queryengine.NewNeo4jExecutor(
		cfg.Neo4jURI,
		cfg.Neo4jUser,
		cfg.Neo4jPassword,
		cfg.Neo4jDatabase,
		cfg.QueryTimeout,
		cfg.MaxRows,
		cfg.MaxResultBytes,
	)
	if err != nil {
		logger.Error("failed to create Neo4j executor", "error", err)
		os.Exit(1)
	}

	connectivityCtx, connectivityCancel := context.WithTimeout(startupCtx, 15*time.Second)
	if err := executor.VerifyConnectivity(connectivityCtx); err != nil {
		connectivityCancel()
		logger.Error("Neo4j connectivity check failed", "error", err)
		os.Exit(1)
	}
	connectivityCancel()

	registry := prometheus.NewRegistry()
	latencies, err := observability.NewQueryLatencies(registry)
	if err != nil {
		logger.Error("failed to register query metrics", "error", err)
		os.Exit(1)
	}
	shutdownTracing, err := observability.SetupTracing(startupCtx, logger)
	if err != nil {
		logger.Error("failed to initialize tracing", "error", err)
		os.Exit(1)
	}
	targetHash := sha256.Sum256([]byte(cfg.Neo4jURI + "\x00" + cfg.Neo4jDatabase))
	schemaCache := schema.NewCache(executor, store, fmt.Sprintf("%x", targetHash), location, logger)
	generator := generation.NewVertexGenerator(
		cfg.GoogleCloudProject,
		cfg.GoogleCloudLocation,
		cfg.VertexModel,
		schemaCache,
		logger,
	)
	logger.Info("query generation configured",
		"model", cfg.VertexModel,
		"project", cfg.GoogleCloudProject,
		"location", cfg.GoogleCloudLocation,
		"prompt_version", generation.PromptVersion,
	)

	service := workspace.NewServiceWithMetrics(generator, executor, store, latencies)
	ui := workspace.NewHTTPHandler(service, logger, schemaCache)

	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("GET /isalive", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /isready", func(w http.ResponseWriter, r *http.Request) {
		checkCtx, checkCancel := context.WithTimeout(r.Context(), time.Second)
		defer checkCancel()
		if err := store.Ping(checkCtx); err != nil {
			http.Error(w, "PostgreSQL unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := executor.VerifyConnectivity(checkCtx); err != nil {
			http.Error(w, "Neo4j unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", ui)

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	refreshCtx, refreshCancel := context.WithCancel(ctx)
	defer refreshCancel()
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		schemaCache.Run(refreshCtx)
	}()
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("query workspace listening", "addr", server.Addr)
		serverErrors <- server.ListenAndServe()
	}()

	var serverErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			serverErr = err
		}
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	refreshCancel()
	<-refreshDone
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown failed", "error", err)
	}
	if err := executor.Close(shutdownCtx); err != nil {
		logger.Error("Neo4j driver shutdown failed", "error", err)
	}
	if err := store.Close(); err != nil {
		logger.Error("PostgreSQL shutdown failed", "error", err)
	}
	traceCtx, traceCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer traceCancel()
	if err := shutdownTracing(traceCtx); err != nil {
		logger.Error("tracing shutdown failed", "error", err)
	}
	if serverErr != nil {
		os.Exit(1)
	}
}
