package workspace

import (
	"context"
	"strings"
	"testing"
)

func TestSavePreservesOriginalQuestion(t *testing.T) {
	catalog := &testCatalog{}
	service := NewService(testGenerator{}, &testExecutor{}, catalog)
	saved, err := service.Save(context.Background(), " PHP repos ", " Languages ",
		" Which non-archived repositories use PHP? ", "RETURN $language", map[string]any{"language": "PHP"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Question != "Which non-archived repositories use PHP?" || saved.Name != "PHP repos" || saved.Category != "Languages" {
		t.Fatalf("saved query lost its question or trimming: %#v", saved)
	}
	if _, err := service.Save(context.Background(), "Too long", "", strings.Repeat("x", 4001), "RETURN 1", nil); err == nil {
		t.Fatal("oversized question was saved")
	}
}

func TestAskRejectsClausesHiddenByBlockComments(t *testing.T) {
	for _, cypher := range []string{
		"WITH 1 AS x /* ' */ CALL db.labels() YIELD label RETURN label /* ' */",
		"WITH 1 AS x /* \" */ CALL db.labels() YIELD label RETURN label /* \" */",
		"WITH 1 AS x /* harmless */ CALL db.labels() YIELD label RETURN label",
		"RETURN 1 /* unterminated",
	} {
		t.Run(cypher, func(t *testing.T) {
			executor := &testExecutor{}
			service := NewService(testGenerator{query: GeneratedQuery{Cypher: cypher}}, executor, &testCatalog{})
			if _, err := service.Ask(context.Background(), "find labels"); err == nil {
				t.Fatal("query should have been rejected")
			}
			if executor.query != "" {
				t.Fatalf("rejected query reached executor: %q", executor.query)
			}
		})
	}
}

func TestQueryPolicyAllowsParameterNamesWithoutAllowingClauses(t *testing.T) {
	for _, test := range []struct {
		cypher string
		valid  bool
	}{
		{"MATCH (n) WHERE n.lastupdated >= $start AND n.lastupdated < $stop RETURN n", true},
		{"RETURN $create, $delete, $call, $`stop`", true},
		{"RETURN '/* literal */', \"CALL\", `CREATE`", true},
		{"RETURN 1 // /* ' CALL db.labels()", true},
		{"RETURN $start CALL db.labels() YIELD label RETURN label", false},
		{"MATCH (n) WHERE n.id = $delete DELETE n", false},
		{"RETURN $stop; RETURN 1", false},
	} {
		t.Run(test.cypher, func(t *testing.T) {
			service := NewService(testGenerator{}, &testExecutor{}, &testCatalog{})
			_, err := service.Save(context.Background(), "Example", "", "", test.cypher, nil)
			if (err == nil) != test.valid {
				t.Fatalf("Save error = %v, want valid = %v", err, test.valid)
			}
		})
	}
}
