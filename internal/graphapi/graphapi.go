package graphapi

// GraphPayload is the JSON response returned by both seed and expand endpoints.
type GraphPayload struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// GraphNode represents a single Neo4j node for the frontend.
type GraphNode struct {
	ID         string         `json:"id"`
	Labels     []string       `json:"labels"`
	Caption    string         `json:"caption"`
	Color      string         `json:"color"`
	HasMore    bool           `json:"hasMore"`
	Properties map[string]any `json:"properties"`
}

// GraphEdge represents a single Neo4j relationship for the frontend.
type GraphEdge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
}

// primaryLabel returns the most specific application-level label from a node's
// label list, ignoring generic ontology labels added by cartography.
func primaryLabel(labels []string) string {
	priority := []string{
		"GitHubRepository", "GitHubUser", "GitHubTeam", "GitHubActionsSecret",
		"GitHubActionsVariable", "GitHubWorkflow", "GitHubEnvironment",
		"GitHubDependabotAlert", "GitHubPersonalAccessToken",
		"NaisApp", "NaisTeam", "NaisMember", "NaisDeployment", "NaisTenant",
		"KubernetesNamespace", "KubernetesIngress", "KubernetesPod",
		"KubernetesContainer", "KubernetesSecret", "KubernetesServiceAccount",
		"KubernetesCluster", "KubernetesNode", "KubernetesService",
		"KubernetesRole", "KubernetesClusterRole",
		"KubernetesRoleBinding", "KubernetesClusterRoleBinding",
	}
	set := make(map[string]bool, len(labels))
	for _, l := range labels {
		set[l] = true
	}
	for _, p := range priority {
		if set[p] {
			return p
		}
	}
	if len(labels) > 0 {
		return labels[0]
	}
	return "Unknown"
}

// caption returns a human-readable display string for a node.
func caption(label string, props map[string]any) string {
	candidates := map[string][]string{
		"GitHubRepository":             {"name"},
		"GitHubUser":                   {"username", "name"},
		"GitHubTeam":                   {"name"},
		"GitHubActionsSecret":          {"name"},
		"GitHubActionsVariable":        {"name"},
		"GitHubWorkflow":               {"name"},
		"GitHubEnvironment":            {"name"},
		"GitHubDependabotAlert":        {"advisory_cve_id", "advisory_ghsa_id"},
		"GitHubPersonalAccessToken":    {"token_name"},
		"NaisApp":                      {"name"},
		"NaisTeam":                     {"slug", "name"},
		"NaisMember":                   {"name", "email"},
		"NaisDeployment":               {"environment_name", "id"},
		"NaisTenant":                   {"id"},
		"KubernetesNamespace":          {"name"},
		"KubernetesIngress":            {"name"},
		"KubernetesPod":                {"name"},
		"KubernetesContainer":          {"name"},
		"KubernetesSecret":             {"name"},
		"KubernetesServiceAccount":     {"name"},
		"KubernetesCluster":            {"name"},
		"KubernetesNode":               {"name"},
		"KubernetesService":            {"name"},
		"KubernetesRole":               {"name"},
		"KubernetesClusterRole":        {"name"},
		"KubernetesRoleBinding":        {"name"},
		"KubernetesClusterRoleBinding": {"name"},
	}
	keys, ok := candidates[label]
	if !ok {
		keys = []string{"name", "id"}
	}
	for _, k := range keys {
		if v, ok := props[k]; ok && v != nil {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return label
}

// nodeColor returns the fill colour for a node based on its primary label.
func nodeColor(label string) string {
	colors := map[string]string{
		"GitHubRepository":             "#2da44e",
		"GitHubUser":                   "#e36209",
		"GitHubTeam":                   "#f0a500",
		"GitHubActionsSecret":          "#cf222e",
		"GitHubActionsVariable":        "#e85aad",
		"GitHubWorkflow":               "#8250df",
		"GitHubEnvironment":            "#0969da",
		"GitHubDependabotAlert":        "#cf222e",
		"GitHubPersonalAccessToken":    "#cf222e",
		"NaisApp":                      "#0067c5",
		"NaisTeam":                     "#0099a8",
		"NaisMember":                   "#66c2ca",
		"NaisDeployment":               "#c9a227",
		"NaisTenant":                   "#004f6e",
		"KubernetesNamespace":          "#6b7fcc",
		"KubernetesIngress":            "#cc6b6b",
		"KubernetesPod":                "#9b72cb",
		"KubernetesContainer":          "#b39ddb",
		"KubernetesSecret":             "#cf222e",
		"KubernetesServiceAccount":     "#e91e8c",
		"KubernetesCluster":            "#455a64",
		"KubernetesService":            "#7986cb",
		"KubernetesRole":               "#ff7043",
		"KubernetesClusterRole":        "#ff5722",
		"KubernetesRoleBinding":        "#ffa726",
		"KubernetesClusterRoleBinding": "#fb8c00",
	}
	if c, ok := colors[label]; ok {
		return c
	}
	return "#9e9e9e"
}

// BuildNode constructs a GraphNode from raw Neo4j node data.
func BuildNode(id string, labels []string, props map[string]any, hasMore bool) GraphNode {
	lbl := primaryLabel(labels)
	return GraphNode{
		ID:         id,
		Labels:     labels,
		Caption:    caption(lbl, props),
		Color:      nodeColor(lbl),
		HasMore:    hasMore,
		Properties: props,
	}
}
