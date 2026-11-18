package schema

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"tpt-graph/query-workspace/internal/modeldiag"
)

const (
	MaxChatTurns             = 8
	MaxQuestionBytes         = 4000
	maxEvidenceNodes         = 24
	maxEvidenceRelationships = 40
	maxEvidenceProperties    = 60
	quotedName               = "`(?:[^`]|``)+`"
)

type Property struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type NodeType struct {
	Name                string     `json:"name"`
	Properties          []Property `json:"properties"`
	PropertiesTruncated bool       `json:"properties_truncated,omitempty"`
}

type RelationshipType struct {
	Source              string     `json:"source"`
	Type                string     `json:"type"`
	Target              string     `json:"target"`
	Properties          []Property `json:"properties"`
	PropertiesTruncated bool       `json:"properties_truncated,omitempty"`
}

type Evidence struct {
	Nodes         []NodeType         `json:"nodes"`
	Relationships []RelationshipType `json:"inferred_relationships"`
	Truncated     bool               `json:"truncated"`
}

type Reply struct {
	Text        string
	Evidence    Evidence
	CapturedAt  time.Time
	Diagnostics *modeldiag.Chat
}

type Turn struct {
	Question string
	Reply    Reply
}

// Index reads the native-v1 snapshot format shared with query generation.
type Index struct {
	nodes         []NodeType
	relationships []RelationshipType
}

func NewIndex(text string) (*Index, error) {
	if len(text) > MaxBytes {
		return nil, errors.New("schema snapshot exceeds size limit")
	}
	_, nodesText, ok := strings.Cut(text, "Node properties (observed in the database; properties may be optional):\n")
	if !ok {
		return nil, errors.New("schema snapshot has an unsupported node section")
	}
	nodesText, rest, ok := strings.Cut(nodesText, "\nRelationship properties (observed in the database):\n")
	if !ok {
		return nil, errors.New("schema snapshot has no relationship property section")
	}
	relsText, patterns, ok := strings.Cut(rest, "\nDirected relationships (metadata-inferred; not verified against actual edges):\n")
	if !ok {
		return nil, errors.New("schema snapshot has no directed relationship section")
	}
	patterns, _, ok = strings.Cut(patterns, "\nMetadata does not establish business meaning or prove an attack path.")
	if !ok {
		return nil, errors.New("schema snapshot has no metadata disclaimer")
	}
	nodes, err := parsePropertyGroups(nodesText)
	if err != nil {
		return nil, err
	}
	relProperties, err := parsePropertyGroups(relsText)
	if err != nil {
		return nil, err
	}
	index := &Index{}
	for name, properties := range nodes {
		index.nodes = append(index.nodes, NodeType{Name: name, Properties: properties})
	}
	if len(index.nodes) == 0 {
		return nil, errors.New("schema snapshot has no node types")
	}
	sort.Slice(index.nodes, func(i, j int) bool { return index.nodes[i].Name < index.nodes[j].Name })
	pattern := regexp.MustCompile(`^\(:(` + quotedName + `)\)-\[:(` + quotedName + `)\]->\(:(` + quotedName + `)\)\n?`)
	for patterns != "" {
		match := pattern.FindStringSubmatch(patterns)
		if match == nil {
			return nil, errors.New("schema snapshot has an invalid directed relationship")
		}
		source, kind, target := unquoteName(match[1]), unquoteName(match[2]), unquoteName(match[3])
		if _, ok := nodes[source]; !ok {
			return nil, fmt.Errorf("schema relationship source %q has no node metadata", source)
		}
		if _, ok := nodes[target]; !ok {
			return nil, fmt.Errorf("schema relationship target %q has no node metadata", target)
		}
		index.relationships = append(index.relationships, RelationshipType{
			Source: source, Type: kind, Target: target, Properties: relProperties[kind],
		})
		patterns = patterns[len(match[0]):]
	}
	return index, nil
}

func parsePropertyGroups(text string) (map[string][]Property, error) {
	text = strings.TrimSpace(text)
	if text != "" {
		text += "\n"
	}
	group := regexp.MustCompile(`^(` + quotedName + `) \{`)
	property := regexp.MustCompile(`^(` + quotedName + `): ([^,}\n]+)`)
	groups := map[string][]Property{}
	for text != "" {
		match := group.FindStringSubmatch(text)
		if match == nil {
			return nil, errors.New("schema snapshot has an invalid property group")
		}
		name := unquoteName(match[1])
		text = text[len(match[0]):]
		properties := []Property{}
		for !strings.HasPrefix(text, "}\n") {
			match = property.FindStringSubmatch(text)
			if match == nil {
				return nil, errors.New("schema snapshot has an invalid property")
			}
			properties = append(properties, Property{unquoteName(match[1]), match[2]})
			text = text[len(match[0]):]
			if strings.HasPrefix(text, ", ") {
				text = text[2:]
			} else if !strings.HasPrefix(text, "}\n") {
				return nil, errors.New("schema snapshot has an invalid property separator")
			}
		}
		groups[name] = properties
		text = text[2:]
	}
	return groups, nil
}

func unquoteName(text string) string {
	return strings.ReplaceAll(text[1:len(text)-1], "``", "`")
}

func (i *Index) EvidenceForLabels(labels []string) (Evidence, error) {
	if len(labels) > maxEvidenceNodes {
		return Evidence{}, errors.New("too many schema label references")
	}
	evidence := Evidence{}
	selected := make(map[string]bool, len(labels))
	for _, label := range labels {
		if selected[label] {
			continue
		}
		for _, node := range i.nodes {
			if node.Name == label {
				evidence.Nodes = append(evidence.Nodes, node)
				selected[label] = true
				break
			}
		}
		if !selected[label] {
			return Evidence{}, fmt.Errorf("node type %q was not found in this schema snapshot", label)
		}
	}
	for _, rel := range i.relationships {
		if selected[rel.Source] || selected[rel.Target] {
			evidence.Relationships = append(evidence.Relationships, rel)
		}
	}
	return i.bound(evidence), nil
}

func (i *Index) bound(evidence Evidence) Evidence {
	if len(evidence.Relationships) > maxEvidenceRelationships {
		evidence.Relationships, evidence.Truncated = evidence.Relationships[:maxEvidenceRelationships], true
	}
	names := map[string]bool{}
	for _, node := range evidence.Nodes {
		names[node.Name] = true
	}
	relationships := make([]RelationshipType, 0, len(evidence.Relationships))
	for _, rel := range evidence.Relationships {
		needed := 0
		if !names[rel.Source] {
			needed++
		}
		if !names[rel.Target] && rel.Target != rel.Source {
			needed++
		}
		if len(names)+needed > maxEvidenceNodes {
			evidence.Truncated = true
			continue
		}
		names[rel.Source], names[rel.Target] = true, true
		rel.Properties, rel.PropertiesTruncated = boundedProperties(rel.Properties)
		relationships = append(relationships, rel)
	}
	evidence.Relationships = relationships
	nodes := make([]NodeType, 0, len(names))
	// Keep referenced types ahead of their neighbours when limiting the display.
	for _, node := range evidence.Nodes {
		node.Properties, node.PropertiesTruncated = boundedProperties(node.Properties)
		nodes = append(nodes, node)
		delete(names, node.Name)
	}
	for _, node := range i.nodes {
		if names[node.Name] {
			node.Properties, node.PropertiesTruncated = boundedProperties(node.Properties)
			nodes = append(nodes, node)
		}
	}
	evidence.Nodes = nodes
	return evidence
}

func boundedProperties(properties []Property) ([]Property, bool) {
	if len(properties) > maxEvidenceProperties {
		return properties[:maxEvidenceProperties], true
	}
	return properties, false
}
