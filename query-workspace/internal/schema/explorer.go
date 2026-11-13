package schema

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	MaxChatTurns           = 8
	MaxQuestionBytes       = 4000
	maxLookupNodes         = 24
	maxLookupRelationships = 40
	maxLookupProperties    = 60
	quotedName             = "`(?:[^`]|``)+`"
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
	Text       string
	Evidence   Evidence
	CapturedAt time.Time
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

// Search matches case-insensitive substrings in labels, property names and edge types.
func (i *Index) Search(query string) (Evidence, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(query) > 200 {
		return Evidence{}, errors.New("schema search requires between 1 and 200 bytes")
	}
	evidence := Evidence{}
	for _, node := range i.nodes {
		if matchesMetadata(query, node.Name, node.Properties) {
			node.Properties = matchingPropertiesFirst(node.Properties, query)
			evidence.Nodes = append(evidence.Nodes, node)
		}
	}
	for _, rel := range i.relationships {
		if matchesMetadata(query, rel.Type, rel.Properties) {
			rel.Properties = matchingPropertiesFirst(rel.Properties, query)
			evidence.Relationships = append(evidence.Relationships, rel)
		}
	}
	return i.bound(evidence), nil
}

func matchingPropertiesFirst(properties []Property, query string) []Property {
	if len(properties) <= maxLookupProperties {
		return properties
	}
	properties = append([]Property(nil), properties...)
	sort.SliceStable(properties, func(i, j int) bool {
		return strings.Contains(strings.ToLower(properties[i].Name), query) &&
			!strings.Contains(strings.ToLower(properties[j].Name), query)
	})
	return properties
}

func matchesMetadata(query, name string, properties []Property) bool {
	if strings.Contains(strings.ToLower(name), query) {
		return true
	}
	for _, property := range properties {
		if strings.Contains(strings.ToLower(property.Name), query) {
			return true
		}
	}
	return false
}

func (i *Index) Inspect(name string) (Evidence, error) {
	evidence := Evidence{}
	for _, node := range i.nodes {
		if node.Name == name {
			evidence.Nodes = append(evidence.Nodes, node)
			break
		}
	}
	if len(evidence.Nodes) == 0 {
		return Evidence{}, fmt.Errorf("node type %q was not found in this schema snapshot", name)
	}
	for _, rel := range i.relationships {
		if rel.Source == name || rel.Target == name {
			evidence.Relationships = append(evidence.Relationships, rel)
		}
	}
	return i.bound(evidence), nil
}

func (i *Index) bound(evidence Evidence) Evidence {
	if len(evidence.Nodes) > maxLookupNodes {
		evidence.Nodes, evidence.Truncated = evidence.Nodes[:maxLookupNodes], true
	}
	if len(evidence.Relationships) > maxLookupRelationships {
		evidence.Relationships, evidence.Truncated = evidence.Relationships[:maxLookupRelationships], true
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
		if len(names)+needed > maxLookupNodes {
			evidence.Truncated = true
			continue
		}
		names[rel.Source], names[rel.Target] = true, true
		rel.Properties, rel.PropertiesTruncated = boundedProperties(rel.Properties, rel.PropertiesTruncated)
		relationships = append(relationships, rel)
	}
	evidence.Relationships = relationships
	nodes := make([]NodeType, 0, len(names))
	// Keep the explicitly inspected node first.
	for _, node := range evidence.Nodes {
		node.Properties, node.PropertiesTruncated = boundedProperties(node.Properties, node.PropertiesTruncated)
		nodes = append(nodes, node)
		delete(names, node.Name)
	}
	for _, node := range i.nodes {
		if names[node.Name] {
			node.Properties, node.PropertiesTruncated = boundedProperties(node.Properties, node.PropertiesTruncated)
			nodes = append(nodes, node)
		}
	}
	evidence.Nodes = nodes
	return evidence
}

func boundedProperties(properties []Property, truncated bool) ([]Property, bool) {
	if len(properties) > maxLookupProperties {
		return properties[:maxLookupProperties], true
	}
	return properties, truncated
}

func (i *Index) Merge(first, second Evidence) Evidence {
	evidence := Evidence{Truncated: first.Truncated || second.Truncated}
	nodes := map[string]bool{}
	relationships := map[[3]string]bool{}
	for _, part := range []Evidence{first, second} {
		for _, node := range part.Nodes {
			if !nodes[node.Name] {
				nodes[node.Name] = true
				evidence.Nodes = append(evidence.Nodes, node)
			}
		}
		for _, rel := range part.Relationships {
			key := [3]string{rel.Source, rel.Type, rel.Target}
			if !relationships[key] {
				relationships[key] = true
				evidence.Relationships = append(evidence.Relationships, rel)
			}
		}
	}
	return i.bound(evidence)
}
