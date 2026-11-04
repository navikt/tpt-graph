package queryengine

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"tpt-graph/query-workspace/internal/queryparams"
	"tpt-graph/query-workspace/internal/workspace"
)

func TestRowCollectorEnforcesRowAndByteLimits(t *testing.T) {
	collector := newRowCollector([]string{"value"}, 2, 20)

	if !collector.add([]any{"first"}) {
		t.Fatal("first row should fit")
	}
	if !collector.add([]any{"second"}) {
		t.Fatal("second row should fit")
	}
	if collector.add([]any{"third"}) {
		t.Fatal("row limit should reject third row")
	}
	if !collector.truncated {
		t.Fatal("row limit should mark results truncated")
	}

	collector = newRowCollector([]string{"value"}, 10, 5)
	if collector.add([]any{"too long"}) {
		t.Fatal("byte limit should reject oversized row")
	}
	if !collector.truncated || len(collector.result().Rows) != 0 {
		t.Fatal("byte limit should mark truncated without storing oversized row")
	}
}

func TestRowCollectorExtractsDistinctGraphEntities(t *testing.T) {
	repo := dbtype.Node{ElementId: "n1", Labels: []string{"GitHubRepository"}, Props: map[string]any{"name": "tpt-graph", "archived": false}}
	language := dbtype.Node{ElementId: "n2", Labels: []string{"ProgrammingLanguage"}, Props: map[string]any{"id": "Go"}}
	unnamed := dbtype.Node{ElementId: "n3", Labels: []string{"Thing"}}
	uses := dbtype.Relationship{ElementId: "r1", Type: "LANGUAGE", StartElementId: "n1", EndElementId: "n2", Props: map[string]any{"size": int64(3)}}
	collector := newRowCollector([]string{"r", "p", "nested"}, 10, 1<<20)

	collector.add([]any{repo, dbtype.Path{Nodes: []dbtype.Node{repo, language}, Relationships: []dbtype.Relationship{uses}}, nil})
	collector.add([]any{language, uses, map[string]any{"items": []any{unnamed, "plain"}}})
	result := collector.result()

	wantRows := [][]string{
		{"(:GitHubRepository) tpt-graph", "(:GitHubRepository) tpt-graph -[:LANGUAGE]-> (:ProgrammingLanguage) Go", ""},
		{"(:ProgrammingLanguage) Go", `{"type":"LANGUAGE","id":"r1","start":"n1","end":"n2","properties":{"size":3}}`, `{"items":["(:Thing) n3","plain"]}`},
	}
	if !reflect.DeepEqual(result.Rows, wantRows) {
		t.Fatalf("rows = %#v, want %#v", result.Rows, wantRows)
	}
	wantNodes := []workspace.GraphNode{
		{ID: "n1", Labels: []string{"GitHubRepository"}, Properties: map[string]string{"name": "tpt-graph", "archived": "false"}},
		{ID: "n2", Labels: []string{"ProgrammingLanguage"}, Properties: map[string]string{"id": "Go"}},
		{ID: "n3", Labels: []string{"Thing"}, Properties: map[string]string{}},
	}
	wantRelationships := []workspace.GraphRelationship{
		{ID: "r1", Type: "LANGUAGE", StartID: "n1", EndID: "n2", Properties: map[string]string{"size": "3"}},
	}
	if !reflect.DeepEqual(result.Graph.Nodes, wantNodes) || !reflect.DeepEqual(result.Graph.Relationships, wantRelationships) {
		t.Fatalf("graph = %#v", result.Graph)
	}
	if want := [][]string{{"n1", "n1", "n2"}, {"n2", "n3"}}; !reflect.DeepEqual(result.Graph.RowNodes, want) {
		t.Fatalf("row nodes = %#v, want %#v", result.Graph.RowNodes, want)
	}
}

func TestRowCollectorCountsGraphEntitiesAgainstOutputBudget(t *testing.T) {
	small := dbtype.Node{ElementId: "n1", Labels: []string{"A"}}
	large := dbtype.Node{ElementId: "n2", Labels: []string{"A"}, Props: map[string]any{"blob": strings.Repeat("x", 400)}}
	collector := newRowCollector([]string{"n"}, 10, 300)

	if !collector.add([]any{small}) || collector.add([]any{large}) {
		t.Fatal("budget must admit the small node and reject the large one")
	}
	result := collector.result()
	if !result.Truncated || len(result.Rows) != 1 || len(result.Graph.Nodes) != 1 || len(result.Graph.RowNodes) != 1 {
		t.Fatalf("rejected row leaked graph entities: %#v", result)
	}
}

func TestRowCollectorCountsRepeatedNodeReferencesAgainstOutputBudget(t *testing.T) {
	node := dbtype.Node{ElementId: "n1", Labels: []string{"A"}}
	repeated := make([]any, 50)
	for i := range repeated {
		repeated[i] = node
	}
	probe := newRowCollector([]string{"nodes"}, 10, 1<<20)
	probe.add([]any{repeated})
	firstRow := probe.bytesUsed
	row, err := encodedSize(probe.rows[0])
	if err != nil {
		t.Fatal(err)
	}

	collector := newRowCollector([]string{"nodes"}, 10, firstRow+row+1)
	if !collector.add([]any{repeated}) || collector.add([]any{repeated}) {
		t.Fatal("node references of a row without new entities were not counted against the budget")
	}
	if result := collector.result(); !result.Truncated || len(result.Graph.RowNodes) != 1 {
		t.Fatalf("rejected row leaked node references: %#v", result.Graph.RowNodes)
	}
}

func TestRowCollectorOmitsGraphForScalarResults(t *testing.T) {
	collector := newRowCollector([]string{"value"}, 10, 1<<20)
	collector.add([]any{"scalar"})
	if graph := collector.result().Graph; graph.Nodes != nil || graph.Relationships != nil || graph.RowNodes != nil {
		t.Fatalf("scalar result has graph data: %#v", graph)
	}
}

type testDriver struct {
	neodriver.DriverWithContext
	session neodriver.SessionWithContext
}

func (d testDriver) NewSession(context.Context, neodriver.SessionConfig) neodriver.SessionWithContext {
	return d.session
}

type testSession struct {
	neodriver.SessionWithContext
	tx neodriver.ExplicitTransaction
}

func (s testSession) BeginTransaction(context.Context, ...func(*neodriver.TransactionConfig)) (neodriver.ExplicitTransaction, error) {
	return s.tx, nil
}

func (s testSession) Close(context.Context) error { return nil }

type testTransaction struct {
	neodriver.ExplicitTransaction
	result      neodriver.ResultWithContext
	rollbackErr error
}

func (t testTransaction) Run(context.Context, string, map[string]any) (neodriver.ResultWithContext, error) {
	return t.result, nil
}

func (t testTransaction) Rollback(context.Context) error { return t.rollbackErr }

type testResult struct {
	neodriver.ResultWithContext
	index int
}

func (r *testResult) Keys() ([]string, error) { return []string{"value"}, nil }
func (r *testResult) Next(context.Context) bool {
	r.index++
	return r.index <= 2
}
func (r *testResult) Record() *neodriver.Record {
	return &neodriver.Record{Values: []any{"collected"}}
}
func (r *testResult) Err() error { return nil }

func TestExecuteRetainsTruncatedResultsWhenRollbackFails(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	traces := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(traces)
	t.Cleanup(func() {
		otel.SetTracerProvider(old)
		_ = traces.Shutdown(context.Background())
	})
	rollbackErr := errors.New("connection lost during rollback")
	executor := &Neo4jExecutor{
		driver: testDriver{session: testSession{tx: testTransaction{
			result: &testResult{}, rollbackErr: rollbackErr,
		}}},
		database: "neo4j", timeout: time.Second, maxRows: 1, maxBytes: 1024,
	}

	result, err := executor.Execute(context.Background(), "RETURN 1", nil)
	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Name() != "neo4j.execute" || spans[0].Status().Code != codes.Error {
		t.Fatalf("expected a failed Neo4j operation span, got %v", spans)
	}

	if !errors.Is(err, rollbackErr) {
		t.Fatalf("want rollback error, got %v", err)
	}
	if !result.Truncated || !reflect.DeepEqual(result.Columns, []string{"value"}) ||
		!reflect.DeepEqual(result.Rows, [][]string{{"collected"}}) {
		t.Fatalf("truncated partial result was lost: %#v", result)
	}
}

func TestNeo4jNumericParametersIntegration(t *testing.T) {
	uri := os.Getenv("QUERY_WORKSPACE_TEST_NEO4J_URI")
	if uri == "" {
		t.Skip("set QUERY_WORKSPACE_TEST_NEO4J_URI to a disposable Neo4j database")
	}
	executor, err := NewNeo4jExecutor(uri, "neo4j", os.Getenv("QUERY_WORKSPACE_TEST_NEO4J_PASSWORD"),
		"neo4j", 10*time.Second, 200, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close(context.Background())
	parameters, err := queryparams.Decode([]byte(`{"limit":1,"id":9007199254740993,"nested":[{"count":9223372036854775807,"score":0.25}]}`))
	if err != nil {
		t.Fatal(err)
	}

	result, err := executor.Execute(context.Background(),
		"UNWIND [1, 2] AS n RETURN $id AS id, $nested AS nested LIMIT $limit", parameters)

	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"9007199254740993", `[{"count":9223372036854775807,"score":0.25}]`}}
	if result.Truncated || !reflect.DeepEqual(result.Rows, want) {
		t.Fatalf("numeric parameter result = %#v, want %#v", result, want)
	}
}
