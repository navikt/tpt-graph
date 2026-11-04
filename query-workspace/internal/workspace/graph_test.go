package workspace

import (
	"fmt"
	"strings"
	"testing"
)

func TestNewGraphViewDrawsOnlyReturnedEntitiesWithinCaps(t *testing.T) {
	if newGraphView(ResultGraph{}) != nil {
		t.Fatal("a result without graph values has no graph")
	}
	graph := ResultGraph{Relationships: []GraphRelationship{
		{ID: "r-missing", Type: "MEMBER_OF", StartID: "n0", EndID: "elsewhere"},
		{ID: "r-beyond-cap", Type: "MEMBER_OF", StartID: "n0", EndID: fmt.Sprintf("n%d", maxDrawnNodes)},
		{ID: "r-drawn", Type: "MEMBER_OF", StartID: "n0", EndID: "n1"},
	}}
	for i := range maxDrawnNodes + 5 {
		graph.Nodes = append(graph.Nodes, GraphNode{ID: fmt.Sprintf("n%d", i), Labels: []string{"GitHubTeam"}, Properties: map[string]string{"slug": fmt.Sprintf("team-%d", i)}})
	}
	view := newGraphView(graph)
	if !view.Drawable || view.TotalNodes != maxDrawnNodes+5 || len(view.Payload.Nodes) != maxDrawnNodes || len(view.Nodes) != maxDrawnNodes {
		t.Fatalf("drawn %d of %d nodes", len(view.Payload.Nodes), view.TotalNodes)
	}
	if len(view.Payload.Edges) != 1 || view.Payload.Edges[0].ID != "r-drawn" {
		t.Fatalf("edges = %#v, want only the relationship with both endpoints drawn", view.Payload.Edges)
	}
	if view.Payload.Nodes[0].Name != "team-0" || view.Legend[0] != (legendEntry{"GitHubTeam", "#C499CE", maxDrawnNodes}) {
		t.Fatalf("node caption or legend wrong: %#v %#v", view.Payload.Nodes[0], view.Legend)
	}
	notices := fmt.Sprint(view.Notices)
	for _, want := range []string{"Graph shows the first 300 of 305 nodes.", "1 returned relationship is not drawn."} {
		if !strings.Contains(notices, want) {
			t.Errorf("missing notice %q in %s", want, notices)
		}
	}
}

func TestNewGraphViewExplainsRelationshipsWithoutNodes(t *testing.T) {
	view := newGraphView(ResultGraph{Relationships: []GraphRelationship{{ID: "r", Type: "LANGUAGE", StartID: "a", EndID: "b"}}})
	if view.Drawable || len(view.Notices) != 1 || view.Notices[0].Title != "No graph can be drawn." {
		t.Fatalf("view = %#v", view)
	}
}
