package graphapi

import (
	"testing"
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
