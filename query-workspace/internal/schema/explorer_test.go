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

func TestEvidenceForLabelsUsesOnlyObservedMetadata(t *testing.T) {
	index, err := NewIndex(explorerFixture)
	if err != nil {
		t.Fatal(err)
	}

	detail, err := index.EvidenceForLabels([]string{"GitHubBranch", "GitHubBranch"})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Nodes) != 2 || len(detail.Relationships) != 1 || detail.Truncated {
		t.Fatalf("missing directed neighbourhood: %#v", detail)
	}
	edge := detail.Relationships[0]
	if edge.Source != "GitHubRepository" || edge.Target != "GitHubBranch" || edge.Type != "HAS_BRANCH" || edge.Properties[0].Name != "lastupdated" {
		t.Fatalf("lost direction or relationship properties: %#v", edge)
	}
	empty, err := index.EvidenceForLabels(nil)
	if err != nil || len(empty.Nodes) != 0 || len(empty.Relationships) != 0 {
		t.Fatalf("invented unreferenced metadata: %#v, %v", empty, err)
	}
	if invalid, err := index.EvidenceForLabels([]string{"GitHubBranch", "InventedLabel"}); err == nil || len(invalid.Nodes) != 0 {
		t.Fatal("unknown labels must be reported explicitly")
	}
	if _, err := index.EvidenceForLabels(make([]string, 25)); err == nil {
		t.Fatal("unbounded references accepted")
	}
}

func TestExplorerHandlesEmptyRelationshipsEscapedNamesAndBounds(t *testing.T) {
	text := "Node properties (observed in the database; properties may be optional):\n`odd``label` {`a,b}`: String}\n"
	for n := range 40 {
		text += fmt.Sprintf("`Type%02d` {`name`: String}\n", n)
	}
	text += "\nRelationship properties (observed in the database):\n`LINK` {}\n" +
		"\nDirected relationships (metadata-inferred; not verified against actual edges):\n"
	for n := range 40 {
		text += fmt.Sprintf("(:`odd``label`)-[:`LINK`]->(:`Type%02d`)\n", n)
	}
	text += "Metadata does not establish business meaning or prove an attack path.\n"
	index, err := NewIndex(text)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := index.EvidenceForLabels([]string{"odd`label", "Type39"})
	if err != nil || len(detail.Nodes) != 24 || !detail.Truncated ||
		detail.Nodes[0].Properties[0].Name != "a,b}" || detail.Nodes[1].Name != "Type39" {
		t.Fatalf("escaped identifiers lost: %#v, %v", detail, err)
	}
	if len(detail.Relationships) > 40 {
		t.Fatal("evidence relationships were not bounded")
	}
	if _, err := NewIndex(strings.ReplaceAll(explorerFixture, "]->", "]--")); err == nil {
		t.Fatal("malformed metadata must not become a partial schema")
	}
}

func TestEvidenceForLabelsPreservesPropertyTruncation(t *testing.T) {
	var properties []string
	for n := range 70 {
		properties = append(properties, fmt.Sprintf("`property%02d`: String", n))
	}
	text := strings.Replace(explorerFixture, "`name`: String, `protected`: Boolean", strings.Join(properties, ", "), 1)
	index, err := NewIndex(text)
	if err != nil {
		t.Fatal(err)
	}
	found, err := index.EvidenceForLabels([]string{"GitHubBranch"})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Nodes) != 2 || !found.Nodes[0].PropertiesTruncated || len(found.Nodes[0].Properties) != 60 {
		t.Fatalf("evidence hid property truncation: %#v", found)
	}
}
