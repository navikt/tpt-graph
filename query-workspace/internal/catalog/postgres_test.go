package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"tpt-graph/query-workspace/internal/schema"
	"tpt-graph/query-workspace/internal/workspace"
)

type queryRow []any

func (r queryRow) Scan(dest ...any) error {
	if len(dest) != len(r) {
		return fmt.Errorf("got %d columns, want %d", len(dest), len(r))
	}
	for i, value := range r {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(value))
	}
	return nil
}

func TestScanSavedQuestionIncludingLegacyRows(t *testing.T) {
	for _, question := range []string{"Which repositories use PHP?", ""} {
		query, err := scanQuery(queryRow{"id", "PHP repos", "Languages", question, "RETURN $language", []byte(`{"language":"PHP"}`), time.Now()})
		if err != nil {
			t.Fatal(err)
		}
		if query.Question != question || query.Cypher != "RETURN $language" || query.Parameters["language"] != "PHP" {
			t.Fatalf("loaded query = %#v", query)
		}
	}
}

func TestCatalogueFailuresAreTracedWithoutQueryData(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(traces)
	t.Cleanup(func() {
		otel.SetTracerProvider(old)
		_ = traces.Shutdown(context.Background())
	})
	db, err := sql.Open("pgx", "postgres://localhost/unused")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := &Postgres{db: db}
	ctx := context.Background()
	_, _ = store.Save(ctx, workspace.SavedQuery{Cypher: "private-query"})
	_, _ = store.Get(ctx, "private-id")
	_, _ = store.List(ctx)
	_, _ = store.LatestSchema(ctx, "private-target")
	_ = store.SaveSchema(ctx, schema.Snapshot{Text: "private-schema"})
	spans := recorder.Ended()
	want := []string{"postgres.save_query", "postgres.get_query", "postgres.list_queries", "postgres.load_schema", "postgres.save_schema"}
	if len(spans) != len(want) {
		t.Fatalf("got %d spans, want %d", len(spans), len(want))
	}
	for i, span := range spans {
		if span.Name() != want[i] || span.Status().Code != codes.Error {
			t.Fatalf("operation %d was not traced as failed: %s %v", i, span.Name(), span.Status())
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.AsString(), "private") {
				t.Fatalf("query data leaked into attribute %s", attr.Key)
			}
		}
	}
}

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
		Question: "Which repositories use PHP?",
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
	if loaded.Cypher != saved.Cypher || loaded.Question != saved.Question || !reflect.DeepEqual(loaded.Parameters, saved.Parameters) {
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
			if query.Question != saved.Question || !reflect.DeepEqual(query.Parameters, saved.Parameters) {
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
