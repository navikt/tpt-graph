package config

import (
	"log/slog"
	"os"
)

// Config holds all runtime configuration for the service.
type Config struct {
	Neo4jURI      string
	Neo4jUser     string
	Neo4jPassword string
	WhodisURL     string
	Port          string
}

// Load reads configuration from environment variables and exits if any required
// variable is missing.
func Load() Config {
	return Config{
		Neo4jURI:      mustEnv("NEO4J_URI"),
		Neo4jUser:     mustEnv("NEO4J_USER"),
		Neo4jPassword: mustEnv("NEO4J_PASSWORD"),
		WhodisURL:     mustEnv("WHODIS_URL"),
		Port:          envOr("PORT", "8080"),
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
