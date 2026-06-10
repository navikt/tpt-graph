package graphapi

import (
	"testing"
)

// --- primaryLabel ---

func TestPrimaryLabel_KnownLabelFirst(t *testing.T) {
	labels := []string{"Base", "GitHubRepository", "Resource"}
	if got := primaryLabel(labels); got != "GitHubRepository" {
		t.Errorf("want %q, got %q", "GitHubRepository", got)
	}
}

func TestPrimaryLabel_PriorityOrdering(t *testing.T) {
	// NaisApp has lower priority than GitHubRepository in the list.
	labels := []string{"NaisApp", "GitHubRepository"}
	if got := primaryLabel(labels); got != "GitHubRepository" {
		t.Errorf("want %q, got %q", "GitHubRepository", got)
	}
}

func TestPrimaryLabel_FallbackToFirstLabel(t *testing.T) {
	labels := []string{"SomeUnknownLabel", "AnotherLabel"}
	if got := primaryLabel(labels); got != "SomeUnknownLabel" {
		t.Errorf("want %q, got %q", "SomeUnknownLabel", got)
	}
}

func TestPrimaryLabel_EmptyLabels(t *testing.T) {
	if got := primaryLabel(nil); got != "Unknown" {
		t.Errorf("want %q, got %q", "Unknown", got)
	}
}

// --- caption ---

func TestCaption_KnownLabelWithProperty(t *testing.T) {
	props := map[string]any{"name": "my-repo"}
	if got := caption("GitHubRepository", props); got != "my-repo" {
		t.Errorf("want %q, got %q", "my-repo", got)
	}
}

func TestCaption_FallbackSecondProperty(t *testing.T) {
	// GitHubUser prefers "username" then "name".
	props := map[string]any{"name": "Alice"}
	if got := caption("GitHubUser", props); got != "Alice" {
		t.Errorf("want %q, got %q", "Alice", got)
	}
}

func TestCaption_PreferredPropertyFirst(t *testing.T) {
	// GitHubUser prefers "username" over "name".
	props := map[string]any{"username": "alice", "name": "Alice"}
	if got := caption("GitHubUser", props); got != "alice" {
		t.Errorf("want %q, got %q", "alice", got)
	}
}

func TestCaption_FallbackToLabelName(t *testing.T) {
	// No matching property — fall back to the label itself.
	props := map[string]any{}
	if got := caption("GitHubRepository", props); got != "GitHubRepository" {
		t.Errorf("want %q, got %q", "GitHubRepository", got)
	}
}

func TestCaption_UnknownLabelFallsBackToNameThenLabel(t *testing.T) {
	props := map[string]any{"name": "something"}
	if got := caption("SomeUnknownLabel", props); got != "something" {
		t.Errorf("want %q, got %q", "something", got)
	}
}

func TestCaption_UnknownLabelNoPropertiesFallsBackToLabel(t *testing.T) {
	props := map[string]any{}
	if got := caption("SomeUnknownLabel", props); got != "SomeUnknownLabel" {
		t.Errorf("want %q, got %q", "SomeUnknownLabel", got)
	}
}

// --- nodeColor ---

func TestNodeColor_KnownLabel(t *testing.T) {
	want := "#2da44e"
	if got := nodeColor("GitHubRepository"); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestNodeColor_UnknownLabel(t *testing.T) {
	want := "#9e9e9e"
	if got := nodeColor("SomeUnknownLabel"); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestNodeColor_HighRiskLabelsAreRed(t *testing.T) {
	red := "#cf222e"
	for _, label := range []string{"GitHubActionsSecret", "GitHubDependabotAlert", "GitHubPersonalAccessToken", "KubernetesSecret"} {
		if got := nodeColor(label); got != red {
			t.Errorf("label %q: want %q, got %q", label, red, got)
		}
	}
}

// --- BuildNode ---

func TestBuildNode_FieldsPopulatedCorrectly(t *testing.T) {
	props := map[string]any{"name": "my-app"}
	node := BuildNode("elem-1", []string{"NaisApp"}, props, true)

	if node.ID != "elem-1" {
		t.Errorf("ID: want %q, got %q", "elem-1", node.ID)
	}
	if node.Caption != "my-app" {
		t.Errorf("Caption: want %q, got %q", "my-app", node.Caption)
	}
	if node.Color != "#0067c5" {
		t.Errorf("Color: want %q, got %q", "#0067c5", node.Color)
	}
	if !node.HasMore {
		t.Error("HasMore: want true, got false")
	}
	if len(node.Labels) != 1 || node.Labels[0] != "NaisApp" {
		t.Errorf("Labels: want [NaisApp], got %v", node.Labels)
	}
}
