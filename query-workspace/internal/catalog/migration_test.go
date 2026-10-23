package catalog

import (
	"context"
	"os"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestQuestionMigrationPreservesLegacyQueriesIntegration(t *testing.T) {
	databaseURL := os.Getenv("QUERY_WORKSPACE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set QUERY_WORKSPACE_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	store, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := goose.DownToContext(ctx, store.db, "migrations", 20261023010000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := goose.UpContext(ctx, store.db, "migrations"); err != nil {
			t.Error(err)
		}
	})
	const legacyID = "legacy-question-migration-test"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO saved_queries (id, name, cypher) VALUES ($1, 'Legacy query', 'RETURN 1')`, legacyID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := store.db.ExecContext(ctx, "DELETE FROM saved_queries WHERE id = $1", legacyID); err != nil {
			t.Error(err)
		}
	}()
	if err := goose.UpContext(ctx, store.db, "migrations"); err != nil {
		t.Fatal(err)
	}
	saved, err := store.Get(ctx, legacyID)
	if err != nil || saved.Question != "" || saved.Cypher != "RETURN 1" {
		t.Fatalf("migration changed the legacy query: %#v, error: %v", saved, err)
	}
}
