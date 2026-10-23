package catalog

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"tpt-graph/query-workspace/internal/schema"
	"tpt-graph/query-workspace/internal/workspace"
)

func TestPostgresCatalogIntegration(t *testing.T) {
	databaseURL := os.Getenv("QUERY_WORKSPACE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set QUERY_WORKSPACE_TEST_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	store, err := NewPostgres(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	saved, err := store.Save(ctx, workspace.SavedQuery{
		Name:     "integration test",
		Category: "Draft",
		Cypher:   "RETURN $name AS name",
		Parameters: map[string]any{
			"name": "workspace-test", "limit": int64(10), "id": int64(9007199254740993),
			"nested": []any{map[string]any{"count": int64(9223372036854775807), "score": float64(0.25)}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = store.db.ExecContext(ctx, "DELETE FROM saved_queries WHERE id = $1", saved.ID)
	}()

	loaded, err := store.Get(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cypher != saved.Cypher || !reflect.DeepEqual(loaded.Parameters, saved.Parameters) {
		t.Fatalf("loaded query = %#v", loaded)
	}
	queries, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, query := range queries {
		if query.ID == saved.ID {
			found = true
			if !reflect.DeepEqual(query.Parameters, saved.Parameters) {
				t.Fatalf("listed parameters = %#v, want %#v", query.Parameters, saved.Parameters)
			}
			break
		}
	}
	if !found {
		t.Fatalf("saved query %q was not visible in catalogue", saved.ID)
	}
}

func TestPostgresSchemaSnapshotsIntegration(t *testing.T) {
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
	target := t.Name()
	t.Cleanup(func() {
		if _, err := store.db.ExecContext(ctx, "DELETE FROM schema_snapshots WHERE target = $1", target); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.LatestSchema(ctx, target); !errors.Is(err, schema.ErrUnavailable) {
		t.Fatalf("want unavailable for empty cache, got %v", err)
	}
	first := schema.Snapshot{
		Target: target, Text: "Dependency {name: String}", Hash: "first",
		Method: "neo4j-native-v1", CapturedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	second := first
	second.Text, second.Hash = "Dependency {name: String, version: String}", "second"
	second.CapturedAt = first.CapturedAt.Add(time.Second)
	for _, snapshot := range []schema.Snapshot{second, first} {
		if err := store.SaveSchema(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := store.LatestSchema(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Text != second.Text || loaded.Hash != second.Hash ||
		loaded.Method != second.Method || !loaded.CapturedAt.Equal(second.CapturedAt) {
		t.Fatalf("latest schema = %#v, want %#v", loaded, second)
	}
	if _, err := store.LatestSchema(ctx, "another-target"); !errors.Is(err, schema.ErrUnavailable) {
		t.Fatalf("snapshot leaked to another database: %v", err)
	}
	var retained int
	if err := store.db.QueryRowContext(ctx, "SELECT count(*) FROM schema_snapshots WHERE target = $1", target).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != 2 {
		t.Fatalf("retained snapshots = %d, want current and previous", retained)
	}
}
