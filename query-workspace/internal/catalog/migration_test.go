package catalog

import (
	"context"
	"os"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestMigrationsRollBackAndReapplyIntegration(t *testing.T) {
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
	if err := goose.DownToContext(ctx, store.db, "migrations", 0); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, store.db, "migrations"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO saved_queries (id, name, kind, cypher) VALUES ('migration-test', 'Kindless', 'graph', 'RETURN 1')`); err == nil {
		t.Fatal("saved query kind is not constrained")
	}
}
