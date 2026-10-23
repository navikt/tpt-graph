package config

import (
	"testing"
	"time"
)

func TestLoadWithDatabaseConfiguration(t *testing.T) {
	for key, value := range map[string]string{
		"DB_URL":         "postgres://localhost/workspace",
		"NEO4J_URI":      "neo4j://localhost:7687",
		"NEO4J_USER":     "workspace",
		"NEO4J_PASSWORD": "secret",
	} {
		t.Setenv(key, value)
	}

	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.Neo4jDatabase != "neo4j" || config.QueryTimeout != 10*time.Second {
		t.Fatalf("unexpected defaults: %#v", config)
	}
}

func TestLoadRejectsMissingDatabaseConfiguration(t *testing.T) {
	t.Setenv("DB_URL", "postgres://localhost/workspace")
	t.Setenv("NEO4J_URI", "neo4j://localhost:7687")
	t.Setenv("NEO4J_USER", "workspace-readonly")
	t.Setenv("NEO4J_PASSWORD", "secret")
	t.Setenv("DB_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("expected missing database configuration error")
	}
}
