package schema

import (
	"fmt"
	"strings"
	"testing"
)

const explorerFixture = "Node properties (observed in the database; properties may be optional):\n" +
	"`GitHubBranch` {`name`: String, `protected`: Boolean}\n" +
	"`GitHubRepository` {`default_branch`: String, `name`: String}\n" +
	"`GCPProject` {`name`: String}\n" +
	"\nRelationship properties (observed in the database):\n" +
	"`HAS_BRANCH` {`lastupdated`: Long}\n" +
	"\nDirected relationships (metadata-inferred; not verified against actual edges):\n" +
	"(:`GitHubRepository`)-[:`HAS_BRANCH`]->(:`GitHubBranch`)\n" +
	"Metadata does not establish business meaning or prove an attack path.\n"

func TestExplorerSearchAndInspectUseOnlyObservedMetadata(t *testing.T) {
	index, err := NewIndex(explorerFixture)
	if err != nil {
		t.Fatal(err)
	}

	found, err := index.Search("branch")
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Nodes) != 2 || len(found.Relationships) != 1 || found.Truncated {
		t.Fatalf("search did not match labels, properties and relationship types: %#v", found)
	}
	detail, err := index.Inspect("GitHubBranch")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 2 || len(detail.Relationships) != 1 {
		t.Fatalf("missing directed neighbourhood: %#v", detail)
	}
	edge := detail.Relationships[0]
	if edge.Source != "GitHubRepository" || edge.Target != "GitHubBranch" || edge.Type != "HAS_BRANCH" || edge.Properties[0].Name != "lastupdated" {
		t.Fatalf("lost direction or relationship properties: %#v", edge)
	}
	missing, err := index.Search("NoSuchService")
	if err != nil || len(missing.Nodes) != 0 || len(missing.Relationships) != 0 {
		t.Fatalf("invented absent metadata: %#v, %v", missing, err)
	}
	if _, err := index.Inspect("InventedLabel"); err == nil {
		t.Fatal("unknown labels must be reported explicitly")
	}
	if _, err := index.Search(strings.Repeat("x", 201)); err == nil {
		t.Fatal("unbounded search accepted")
	}
}

func TestExplorerHandlesEmptyRelationshipsEscapedNamesAndBounds(t *testing.T) {
	text := "Node properties (observed in the database; properties may be optional):\n`odd``label` {`a,b}`: String}\n"
	for n := range 40 {
		text += fmt.Sprintf("`Type%02d` {`name`: String}\n", n)
	}
	text += "\nRelationship properties (observed in the database):\n\n" +
		"\nDirected relationships (metadata-inferred; not verified against actual edges):\n\n" +
		"Metadata does not establish business meaning or prove an attack path.\n"
	index, err := NewIndex(text)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := index.Inspect("odd`label")
	if err != nil || len(detail.Nodes) != 1 || detail.Nodes[0].Properties[0].Name != "a,b}" {
		t.Fatalf("escaped identifiers lost: %#v, %v", detail, err)
	}
	found, err := index.Search("Type")
	if err != nil || !found.Truncated || len(found.Nodes) != 24 {
		t.Fatalf("lookup was not explicitly bounded: %#v, %v", found, err)
	}
	if _, err := NewIndex(strings.ReplaceAll(explorerFixture, "]->", "]--")); err == nil {
		t.Fatal("malformed metadata must not become a partial schema")
	}
}

func TestExplorerPreservesPropertyTruncationWhenCombiningToolEvidence(t *testing.T) {
	var properties []string
	for n := range 70 {
		properties = append(properties, fmt.Sprintf("`property%02d`: String", n))
	}
	text := strings.Replace(explorerFixture, "`name`: String, `protected`: Boolean", strings.Join(properties, ", "), 1)
	index, err := NewIndex(text)
	if err != nil {
		t.Fatal(err)
	}
	found, err := index.Inspect("GitHubBranch")
	if err != nil {
		t.Fatal(err)
	}
	combined := index.Merge(found, found)
	if len(combined.Nodes) != 2 || !combined.Nodes[0].PropertiesTruncated || len(combined.Nodes[0].Properties) != 60 {
		t.Fatalf("merged evidence hid truncation or duplicated metadata: %#v", combined)
	}
	found, err = index.Search("property69")
	if err != nil || len(found.Nodes) != 1 || found.Nodes[0].Properties[0].Name != "property69" {
		t.Fatalf("search hid the property that matched beyond the display limit: %#v, %v", found, err)
	}
}
