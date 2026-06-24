package graphapi

import (
	"fmt"
	"strings"
	"testing"

	neo4j "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func TestPrimaryLabel(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{
			name:   "selects known label from mixed list",
			labels: []string{"Base", "GitHubRepository", "Resource"},
			want:   "GitHubRepository",
		},
		{
			name:   "respects priority order",
			labels: []string{"NaisApp", "GitHubRepository"},
			want:   "GitHubRepository",
		},
		{
			name:   "falls back to first label when no known label present",
			labels: []string{"SomeUnknownLabel", "AnotherLabel"},
			want:   "SomeUnknownLabel",
		},
		{
			name:   "returns Unknown for empty labels",
			labels: nil,
			want:   "Unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := primaryLabel(tt.labels); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestCaption(t *testing.T) {
	tests := []struct {
		name  string
		label string
		props map[string]any
		want  string
	}{
		{
			name:  "known label uses mapped property",
			label: "GitHubRepository",
			props: map[string]any{"name": "my-repo"},
			want:  "my-repo",
		},
		{
			name:  "prefers first candidate property",
			label: "GitHubUser",
			props: map[string]any{"username": "alice", "name": "Alice"},
			want:  "alice",
		},
		{
			name:  "falls back to second candidate when first is absent",
			label: "GitHubUser",
			props: map[string]any{"name": "Alice"},
			want:  "Alice",
		},
		{
			name:  "falls back to label name when no property matches",
			label: "GitHubRepository",
			props: map[string]any{},
			want:  "GitHubRepository",
		},
		{
			name:  "unknown label uses name property when present",
			label: "SomeUnknownLabel",
			props: map[string]any{"name": "something"},
			want:  "something",
		},
		{
			name:  "unknown label falls back to label name",
			label: "SomeUnknownLabel",
			props: map[string]any{},
			want:  "SomeUnknownLabel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := caption(tt.label, tt.props); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestNodeColor(t *testing.T) {
	tests := []struct {
		name  string
		label string
		want  string
	}{
		{
			name:  "known label returns mapped colour",
			label: "GitHubRepository",
			want:  "#2da44e",
		},
		{
			name:  "unknown label returns default grey",
			label: "SomeUnknownLabel",
			want:  "#9e9e9e",
		},
		{
			name:  "GitHubActionsSecret is red",
			label: "GitHubActionsSecret",
			want:  "#cf222e",
		},
		{
			name:  "GitHubDependabotAlert is red",
			label: "GitHubDependabotAlert",
			want:  "#cf222e",
		},
		{
			name:  "KubernetesSecret is red",
			label: "KubernetesSecret",
			want:  "#cf222e",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeColor(tt.label); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestBuildNode(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		labels      []string
		props       map[string]any
		hasMore     bool
		wantCaption string
		wantColor   string
		wantHasMore bool
	}{
		{
			name:        "NaisApp node is populated correctly",
			id:          "elem-1",
			labels:      []string{"NaisApp"},
			props:       map[string]any{"name": "my-app"},
			hasMore:     true,
			wantCaption: "my-app",
			wantColor:   "#0067c5",
			wantHasMore: true,
		},
		{
			name:        "hasMore false is preserved",
			id:          "elem-2",
			labels:      []string{"NaisTeam"},
			props:       map[string]any{"slug": "appsec"},
			hasMore:     false,
			wantCaption: "appsec",
			wantColor:   "#0099a8",
			wantHasMore: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := BuildNode(tt.id, tt.labels, tt.props, tt.hasMore)
			if node.ID != tt.id {
				t.Errorf("ID: want %q, got %q", tt.id, node.ID)
			}
			if node.Caption != tt.wantCaption {
				t.Errorf("Caption: want %q, got %q", tt.wantCaption, node.Caption)
			}
			if node.Color != tt.wantColor {
				t.Errorf("Color: want %q, got %q", tt.wantColor, node.Color)
			}
			if node.HasMore != tt.wantHasMore {
				t.Errorf("HasMore: want %v, got %v", tt.wantHasMore, node.HasMore)
			}
		})
	}
}

func TestGraphSeedQuery_FiltersActiveDeployments(t *testing.T) {
	if !strings.Contains(graphSeedQuery, "is_active") {
		t.Error("graphSeedQuery must filter NaisDeployment nodes by is_active = true to exclude inactive deployments")
	}
}

func TestGraphSeedQuery_UsesRunsImageNotRunsIn(t *testing.T) {
	if !strings.Contains(graphSeedQuery, "RUNS_IMAGE") {
		t.Error("graphSeedQuery must use RUNS_IMAGE to ground deployments in actual running containers, not RUNS_IN")
	}
	if strings.Contains(graphSeedQuery, "RUNS_IN") {
		t.Error("graphSeedQuery must not use RUNS_IN: that relationship exists for all clusters regardless of whether the app is actually running")
	}
}

func TestGraphSeedQuery_ReturnsContainerEdges(t *testing.T) {
	if !strings.Contains(graphSeedQuery, "r5") || !strings.Contains(graphSeedQuery, "r6") {
		t.Error("graphSeedQuery must bind CONTAINS and RESOURCE relationships to named variables (r5, r6) so they are returned as edges")
	}
	if strings.Contains(graphSeedQuery, "<-[:CONTAINS]") || strings.Contains(graphSeedQuery, "<-[:RESOURCE]") {
		t.Error("graphSeedQuery must not use anonymous relationships for CONTAINS/RESOURCE — they will be dropped from the payload")
	}
}

func TestGraphExpandQuery_FiltersActiveDeployments(t *testing.T) {
	if !strings.Contains(graphExpandQuery, "is_active") {
		t.Error("graphExpandQuery must filter NaisDeployment nodes by is_active = true to exclude inactive deployments")
	}
}

func TestCapNodesByLabel(t *testing.T) {
	makeNode := func(id string, labels []string) neo4j.Node {
		return neo4j.Node{ElementId: id, Labels: labels}
	}

	tests := []struct {
		name      string
		nodes     map[string]neo4j.Node
		limit     int
		wantTotal int
		wantMax   int // max nodes of any single label
	}{
		{
			name: "nodes under limit are unchanged",
			nodes: map[string]neo4j.Node{
				"a": makeNode("a", []string{"GitHubRepository"}),
				"b": makeNode("b", []string{"NaisApp"}),
			},
			limit:     10,
			wantTotal: 2,
			wantMax:   1,
		},
		{
			name: "nodes over limit are capped per label",
			nodes: func() map[string]neo4j.Node {
				m := make(map[string]neo4j.Node, 15)
				for i := range 15 {
					id := fmt.Sprintf("dep-%d", i)
					m[id] = makeNode(id, []string{"Dependency"})
				}
				return m
			}(),
			limit:     10,
			wantTotal: 10,
			wantMax:   10,
		},
		{
			name: "different labels are capped independently",
			nodes: func() map[string]neo4j.Node {
				m := make(map[string]neo4j.Node, 20)
				for i := range 12 {
					id := fmt.Sprintf("dep-%d", i)
					m[id] = makeNode(id, []string{"Dependency"})
				}
				for i := range 8 {
					id := fmt.Sprintf("user-%d", i)
					m[id] = makeNode(id, []string{"GitHubUser"})
				}
				return m
			}(),
			limit:     10,
			wantTotal: 18, // 10 Dependency + 8 GitHubUser
			wantMax:   10,
		},
		{
			name:      "empty map returns empty map",
			nodes:     map[string]neo4j.Node{},
			limit:     10,
			wantTotal: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := capNodesByLabel(tt.nodes, tt.limit)
			if len(got) != tt.wantTotal {
				t.Errorf("total nodes: want %d, got %d", tt.wantTotal, len(got))
			}
			// Verify no label exceeds the limit.
			labelCounts := make(map[string]int)
			for _, n := range got {
				labelCounts[primaryLabel(n.Labels)]++
			}
			for label, count := range labelCounts {
				if count > tt.limit {
					t.Errorf("label %q: got %d nodes, limit is %d", label, count, tt.limit)
				}
			}
		})
	}
}
