package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"tpt-graph/internal/config"
	"tpt-graph/internal/graphapi"
	"tpt-graph/internal/handler"
	"tpt-graph/internal/neo4j"
	"tpt-graph/internal/telemetry"
	"tpt-graph/internal/whodis"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(log)

	cfg := config.Load()

	shutdown, err := telemetry.Setup(context.Background())
	if err != nil {
		log.Error("failed to set up telemetry", "err", err)
		os.Exit(1)
	}

	client, err := neo4j.NewClient(cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPassword)
	if err != nil {
		log.Error("failed to create neo4j client", "err", err)
		os.Exit(1)
	}

	connCtx, connCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer connCancel()
	if err := client.VerifyConnectivity(connCtx); err != nil {
		log.Error("neo4j connectivity check failed", "err", err)
		os.Exit(1)
	}
	log.Info("connected to neo4j", "uri", cfg.Neo4jURI)

	whodisClient := whodis.NewClient(cfg.WhodisURL)

	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

	mux := http.NewServeMux()
	mux.HandleFunc("/isready", ok)
	mux.HandleFunc("/isalive", ok)
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/static/", handler.StaticHandler())
	mux.Handle("/api/graph/", graphapi.NewHandler(client))
	mux.Handle("/", handler.New(client, whodisClient))

	server := &http.Server{
		Addr:              fmt.Sprintf(":%s", cfg.Port),
		Handler:           otelhttp.NewHandler(mux, ""),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Info("server listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "err", err)
	}

	if err := shutdown(context.Background()); err != nil {
		log.Error("telemetry shutdown failed", "err", err)
	}

	client.Close(context.Background())
}
