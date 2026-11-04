package workspace

import (
	"fmt"
	"hash/fnv"
	"sort"
)

const (
	maxDrawnNodes         = 300
	maxDrawnRelationships = 2000
)

var knownLabelColors = map[string]string{
	"GitHubRepository":    "#79A9DB",
	"ProgrammingLanguage": "#E7B369",
	"GitHubOrganization":  "#7CBC92",
	"GitHubTeam":          "#C499CE",
}

// Other labels share these hues (≈ oklch L 0.74–0.78, C 0.09–0.10) as hex, because Cytoscape can't parse oklch.
var extraLabelColors = []string{"#EA9891", "#64C3C8", "#9EA4E9", "#BBBD70"}

const unlabelledColor = "#A7ADB5"

func labelColor(label string) string {
	if color, ok := knownLabelColors[label]; ok {
		return color
	}
	if label == "" {
		return unlabelledColor
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(label))
	return extraLabelColors[hash.Sum32()%uint32(len(extraLabelColors))]
}

// NodeCaption picks the most human-readable identifying property, falling back to the element ID.
func NodeCaption(elementID string, properties map[string]string) string {
	for _, key := range []string{"name", "login", "slug", "title", "id"} {
		if value := properties[key]; value != "" {
			return value
		}
	}
	return elementID
}

type graphView struct {
	Drawable   bool
	Legend     []legendEntry
	Nodes      []graphNodeView
	Notices    []noticeView
	Payload    graphPayload
	TotalNodes int
}

type legendEntry struct {
	Label, Color string
	Count        int
}

type graphNodeView struct {
	ID, Label, Name string
}

type graphPayload struct {
	Nodes []payloadNode `json:"nodes"`
	Edges []payloadEdge `json:"edges"`
}

type payloadNode struct {
	ID         string      `json:"id"`
	Label      string      `json:"label"`
	Name       string      `json:"name"`
	Color      string      `json:"color"`
	Properties [][2]string `json:"properties"`
}

type payloadEdge struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Source string `json:"source"`
	Target string `json:"target"`
}

// newGraphView draws only entities the query returned: relationships need both endpoints in the
// drawn node set, and no extra data is fetched to complete them.
func newGraphView(graph ResultGraph) *graphView {
	if len(graph.Nodes) == 0 && len(graph.Relationships) == 0 {
		return nil
	}
	view := &graphView{TotalNodes: len(graph.Nodes), Payload: graphPayload{Nodes: []payloadNode{}, Edges: []payloadEdge{}}}
	returned := make(map[string]bool, len(graph.Nodes))
	for _, node := range graph.Nodes {
		returned[node.ID] = true
	}
	drawn := make(map[string]bool, min(len(graph.Nodes), maxDrawnNodes))
	counts := make(map[string]int)
	for _, node := range graph.Nodes[:min(len(graph.Nodes), maxDrawnNodes)] {
		drawn[node.ID] = true
		label := primaryLabel(node)
		counts[label]++
		name := NodeCaption(node.ID, node.Properties)
		keys := make([]string, 0, len(node.Properties))
		for key := range node.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		properties := make([][2]string, 0, len(keys))
		for _, key := range keys {
			properties = append(properties, [2]string{key, node.Properties[key]})
		}
		view.Payload.Nodes = append(view.Payload.Nodes, payloadNode{node.ID, label, name, labelColor(label), properties})
		view.Nodes = append(view.Nodes, graphNodeView{node.ID, label, name})
	}
	view.Drawable = len(drawn) > 0
	missingEndpoints, eligible := 0, 0
	for _, relationship := range graph.Relationships {
		switch {
		case !returned[relationship.StartID] || !returned[relationship.EndID]:
			missingEndpoints++
		case drawn[relationship.StartID] && drawn[relationship.EndID]:
			eligible++
			if eligible <= maxDrawnRelationships {
				view.Payload.Edges = append(view.Payload.Edges, payloadEdge{relationship.ID, relationship.Type, relationship.StartID, relationship.EndID})
			}
		}
	}
	labels := make([]string, 0, len(counts))
	for label := range counts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		view.Legend = append(view.Legend, legendEntry{label, labelColor(label), counts[label]})
	}

	endpointAdvice := "Return both endpoint nodes as well, or return paths — for example MATCH p = (a)-[r]->(b) RETURN p."
	if !view.Drawable {
		view.Notices = append(view.Notices, noticeView{"info", "status", "i", "No graph can be drawn.",
			"This query returned relationships but none of their nodes, and the workspace does not fetch extra data to fill them in. " + endpointAdvice, ""})
		return view
	}
	if len(graph.Nodes) > maxDrawnNodes {
		view.Notices = append(view.Notices, noticeView{"warn", "status", "▲",
			fmt.Sprintf("Graph shows the first %d of %d nodes.", maxDrawnNodes, len(graph.Nodes)),
			"The table has all collected rows. Relationships to nodes beyond this drawing limit are not drawn.", ""})
	}
	if eligible > maxDrawnRelationships {
		view.Notices = append(view.Notices, noticeView{"warn", "status", "▲",
			fmt.Sprintf("Graph shows the first %d of %d drawable relationships.", maxDrawnRelationships, eligible),
			"The table has all collected rows.", ""})
	}
	if missingEndpoints > 0 {
		view.Notices = append(view.Notices, noticeView{"info", "status", "i",
			fmt.Sprintf("%d returned %s not drawn.", missingEndpoints, plural(missingEndpoints, "relationship is", "relationships are")),
			"Their start or end node was not returned, and the workspace does not fetch extra data. " + endpointAdvice, ""})
	}
	return view
}

func primaryLabel(node GraphNode) string {
	if len(node.Labels) == 0 {
		return ""
	}
	return node.Labels[0]
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}
