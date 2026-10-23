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
)

type metadataResult struct {
	records []*neodriver.Record
	index   int
	err     error
}

func (r *metadataResult) Next(context.Context) bool {
	if r.index >= len(r.records) {
		return false
	}
	r.index++
	return true
}

func (r *metadataResult) Record() *neodriver.Record { return r.records[r.index-1] }
func (r *metadataResult) Err() error                { return r.err }

func metadataRecord(values map[string]any) *neodriver.Record {
	record := &neodriver.Record{}
	for key, value := range values {
		record.Keys = append(record.Keys, key)
		record.Values = append(record.Values, value)
	}
	return record
}

func TestNativeSchemaCapturesPropertiesAndInferredDirections(t *testing.T) {
	var queries []string
	run := func(query string) (metadataRows, error) {
		queries = append(queries, query)
		switch query {
		case nodePropertiesQuery:
			return &metadataResult{records: []*neodriver.Record{
				metadataRecord(map[string]any{"nodeLabels": []any{"GitHubDependency", "Dependency"}, "propertyName": "name", "propertyTypes": []any{"String"}}),
				metadataRecord(map[string]any{"nodeLabels": []any{"GitHubRepository"}, "propertyName": "name", "propertyTypes": []any{"String"}}),
			}}, nil
		case relationshipPropertiesQuery:
			return &metadataResult{records: []*neodriver.Record{
				metadataRecord(map[string]any{"relType": ":`REQUIRES`", "propertyName": "lastupdated", "propertyTypes": []any{"Long"}}),
			}}, nil
		case visualizationQuery:
			return &metadataResult{records: []*neodriver.Record{
				metadataRecord(map[string]any{
					"nodes": []any{
						neodriver.Node{Id: -1, Props: map[string]any{"name": "GitHubRepository"}},
						neodriver.Node{Id: -2, Props: map[string]any{"name": "Dependency"}},
					},
					"relationships": []any{neodriver.Relationship{StartId: -1, EndId: -2, Type: "REQUIRES"}},
				}),
			}}, nil
		default:
			t.Fatalf("unexpected discovery query: %s", query)
			return nil, nil
		}
	}
	text, err := readNativeSchema(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`Dependency` {`name`: String}", "`GitHubDependency` {`name`: String}",
		"`REQUIRES` {`lastupdated`: Long}",
		"(:`GitHubRepository`)-[:`REQUIRES`]->(:`Dependency`)",
		"metadata-inferred; not verified against actual edges",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in schema:\n%s", want, text)
		}
	}
	if !reflect.DeepEqual(queries, []string{nodePropertiesQuery, relationshipPropertiesQuery, visualizationQuery}) {
		t.Fatalf("discovery queries = %#v", queries)
	}
}

func TestNativeSchemaRejectsEmptyOrFailedDiscovery(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"empty graph", nil},
		{"stream failure", context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			text, err := readNativeSchema(context.Background(), func(string) (metadataRows, error) {
				return &metadataResult{err: test.err}, nil
			})
			if err == nil || text != "" {
				t.Fatalf("want failed discovery without partial schema, got %q, %v", text, err)
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatalf("lost stream error: %v", err)
			}
		})
	}
}

func TestNeo4jSchemaIntegration(t *testing.T) {
	uri := os.Getenv("QUERY_WORKSPACE_TEST_NEO4J_URI")
	if uri == "" {
		t.Skip("set QUERY_WORKSPACE_TEST_NEO4J_URI to a disposable Neo4j database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	password := os.Getenv("QUERY_WORKSPACE_TEST_NEO4J_PASSWORD")
	driver, err := neodriver.NewDriverWithContext(uri, neodriver.BasicAuth("neo4j", password, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close(context.Background())
	_, err = neodriver.ExecuteQuery(ctx, driver,
		"CREATE (:WorkspaceSchemaFixtureSource {name: 'synthetic'})-[:WORKSPACE_SCHEMA_FIXTURE {lastupdated: 1}]->(:WorkspaceSchemaFixtureTarget {version: '1.0'})",
		nil, neodriver.EagerResultTransformer, neodriver.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := neodriver.ExecuteQuery(context.Background(), driver,
			"MATCH (n) WHERE n:WorkspaceSchemaFixtureSource OR n:WorkspaceSchemaFixtureTarget DETACH DELETE n",
			nil, neodriver.EagerResultTransformer, neodriver.ExecuteQueryWithDatabase("neo4j"))
		if err != nil {
			t.Error(err)
		}
	}()
	executor, err := NewNeo4jExecutor(uri, "neo4j", password, "neo4j", 10*time.Second, 200, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close(context.Background())
	text, err := executor.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`WorkspaceSchemaFixtureSource` {`name`: String}",
		"`WorkspaceSchemaFixtureTarget` {`version`: String}",
		"(:`WorkspaceSchemaFixtureSource`)-[:`WORKSPACE_SCHEMA_FIXTURE`]->(:`WorkspaceSchemaFixtureTarget`)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in live schema:\n%s", want, text)
		}
	}
	if strings.Contains(text, "synthetic") || strings.Contains(text, "1.0") {
		t.Fatalf("property values leaked into schema: %s", text)
	}
}
