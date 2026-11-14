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

func TestLoadKeepsChatModelIndependentOfQueryModel(t *testing.T) {
	for key, value := range map[string]string{
		"DB_URL":         "postgres://localhost/workspace",
		"NEO4J_URI":      "neo4j://localhost:7687",
		"NEO4J_USER":     "workspace",
		"NEO4J_PASSWORD": "secret",
		"VERTEX_MODEL":   "query-model",
	} {
		t.Setenv(key, value)
	}
	for _, test := range []struct {
		name, configured, want string
	}{
		{"default", "", "gemini-3.8-flash"},
		{"override", "chat-model", "chat-model"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("VERTEX_CHAT_MODEL", test.configured)
			config, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if config.VertexModel != "query-model" || config.VertexChatModel != test.want {
				t.Fatalf("model settings are not independent: %#v", config)
			}
		})
	}
}
