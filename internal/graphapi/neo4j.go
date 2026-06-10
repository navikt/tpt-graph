package graphapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// GraphQuerier is the minimal interface the graphapi package needs from the Neo4j client.
type GraphQuerier interface {
	GraphSeed(ctx context.Context, repo string) (*GraphPayload, error)
	GraphExpand(ctx context.Context, elementID string, knownIDs []string) (*GraphPayload, error)
}

// graphSeedQuery is the Cypher query used by GraphSeed.
// Exposed as a variable so tests can assert its content.
var graphSeedQuery = `
	MATCH (repo:GitHubRepository {name: $repo})
	OPTIONAL MATCH (repo)<-[r1:DEPLOYED_FROM]-(d:NaisDeployment)
	              <-[r2:HAS_DEPLOYMENT]-(app:NaisApp)
	WHERE d.is_active = true
	OPTIONAL MATCH (app)-[r3:RUNS_IN]->(ns:KubernetesNamespace)
	OPTIONAL MATCH (app)<-[r4:HAS_APP]-(team:NaisTeam)
	RETURN repo, d, app, ns, team, r1, r2, r3, r4`

func GraphSeed(ctx context.Context, drv neodriver.DriverWithContext, repo string) (*GraphPayload, error) {
	session := drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, graphSeedQuery,
		map[string]any{"repo": repo},
	)
	if err != nil {
		return nil, fmt.Errorf("graph seed query: %w", err)
	}

	nodes := map[string]neo4j.Node{}
	rels := map[string]neo4j.Relationship{}

	for result.Next(ctx) {
		rec := result.Record()
		for _, key := range []string{"repo", "d", "app", "ns", "team"} {
			val, ok := rec.Get(key)
			if !ok || val == nil {
				continue
			}
			if n, ok := val.(neo4j.Node); ok {
				nodes[n.ElementId] = n
			}
		}
		for _, key := range []string{"r1", "r2", "r3", "r4"} {
			val, ok := rec.Get(key)
			if !ok || val == nil {
				continue
			}
			if r, ok := val.(neo4j.Relationship); ok {
				rels[r.ElementId] = r
			}
		}
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("graph seed iteration: %w", err)
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	nodeIDs := make([]string, 0, len(nodes))
	for id := range nodes {
		nodeIDs = append(nodeIDs, id)
	}
	// For the seed, all returned nodes are "known" after this call — pass
	// nodeIDs as both the query targets and the known set so hasMore reflects
	// neighbours beyond this initial payload.
	counts, err := batchUnexploredCounts(ctx, drv, nodeIDs, nodeIDs)
	if err != nil {
		return nil, err
	}

	return buildPayload(nodes, rels, counts), nil
}

// graphExpandQuery is the Cypher query used by GraphExpand.
// Exposed as a variable so tests can assert its content.
// NaisDeployment nodes with is_active = false are excluded to prevent
// historical deployments from flooding the graph on expand.
var graphExpandQuery = `
	MATCH (n) WHERE elementId(n) = $id
	MATCH (n)-[r]-(neighbour)
	WHERE NOT (neighbour:NaisDeployment AND neighbour.is_active = false)
	RETURN r, neighbour`

// GraphExpand returns all immediate neighbours of a node, excluding already-known nodes.
func GraphExpand(ctx context.Context, drv neodriver.DriverWithContext, elementID string, knownIDs []string) (*GraphPayload, error) {
	session := drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, graphExpandQuery,
		map[string]any{"id": elementID},
	)
	if err != nil {
		return nil, fmt.Errorf("graph expand query: %w", err)
	}

	known := make(map[string]bool, len(knownIDs))
	for _, id := range knownIDs {
		known[id] = true
	}

	nodes := map[string]neo4j.Node{}
	rels := map[string]neo4j.Relationship{}

	for result.Next(ctx) {
		rec := result.Record()
		if val, ok := rec.Get("neighbour"); ok && val != nil {
			if n, ok := val.(neo4j.Node); ok {
				nodes[n.ElementId] = n
			}
		}
		if val, ok := rec.Get("r"); ok && val != nil {
			if rel, ok := val.(neo4j.Relationship); ok {
				// Always collect the relationship — the frontend skips edges
				// whose endpoints are already in the graph, so known→new
				// connections are handled correctly by Cytoscape.
				rels[rel.ElementId] = rel
			}
		}
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("graph expand iteration: %w", err)
	}

	// Filter out nodes the client already has, but keep all edges.
	for id := range nodes {
		if known[id] {
			delete(nodes, id)
		}
	}

	nodeIDs := make([]string, 0, len(nodes))
	for id := range nodes {
		nodeIDs = append(nodeIDs, id)
	}
	// knownIDs includes both the expanded node and all pre-existing nodes passed
	// by the client — so hasMore only fires for truly unexplored neighbours.
	allKnown := append(knownIDs, elementID)
	allKnown = append(allKnown, nodeIDs...)
	counts, err := batchUnexploredCounts(ctx, drv, nodeIDs, allKnown)
	if err != nil {
		return nil, err
	}

	return buildPayload(nodes, rels, counts), nil
}

// batchUnexploredCounts returns, for each node ID, the count of neighbours
// that are NOT in the knownIDs set. This is used to set hasMore correctly —
// a node has more to explore only if it has neighbours the client hasn't seen.
func batchUnexploredCounts(ctx context.Context, drv neodriver.DriverWithContext, ids []string, knownIDs []string) (map[string]int64, error) {
	if len(ids) == 0 {
		return map[string]int64{}, nil
	}
	session := drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, `
		UNWIND $ids AS eid
		MATCH (n) WHERE elementId(n) = eid
		MATCH (n)-[]-(neighbour)
		WHERE NOT elementId(neighbour) IN $knownIds
		RETURN elementId(n) AS id, count(DISTINCT neighbour) AS unexplored`,
		map[string]any{"ids": ids, "knownIds": knownIDs},
	)
	if err != nil {
		return nil, fmt.Errorf("unexplored count query: %w", err)
	}

	counts := make(map[string]int64, len(ids))
	for result.Next(ctx) {
		rec := result.Record()
		id, _ := rec.Get("id")
		cnt, _ := rec.Get("unexplored")
		if idStr, ok := id.(string); ok {
			if cntInt, ok := cnt.(int64); ok {
				counts[idStr] = cntInt
			}
		}
	}
	return counts, result.Err()
}

// buildPayload converts raw Neo4j nodes and relationships into a GraphPayload.
func buildPayload(nodes map[string]neo4j.Node, rels map[string]neo4j.Relationship, degrees map[string]int64) *GraphPayload {
	payload := &GraphPayload{
		Nodes: make([]GraphNode, 0, len(nodes)),
		Edges: make([]GraphEdge, 0, len(rels)),
	}

	for _, n := range nodes {
		props := make(map[string]any, len(n.Props))
		for k, v := range n.Props {
			props[k] = cleanProp(v)
		}
		hasMore := degrees[n.ElementId] > 0
		payload.Nodes = append(payload.Nodes, BuildNode(n.ElementId, n.Labels, props, hasMore))
	}

	for _, r := range rels {
		payload.Edges = append(payload.Edges, GraphEdge{
			ID:     r.ElementId,
			Source: r.StartElementId,
			Target: r.EndElementId,
			Type:   r.Type,
		})
	}

	return payload
}

// cleanProp converts Neo4j property values to JSON-safe Go types.
func cleanProp(v any) any {
	switch val := v.(type) {
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = cleanProp(item)
		}
		return out
	case neo4j.LocalDateTime:
		return val.Time().UTC().Format("2006-01-02T15:04:05Z")
	case neo4j.Date:
		return val.Time().UTC().Format("2006-01-02")
	case neo4j.Duration:
		return val.String()
	case []byte:
		return strings.TrimSpace(string(val))
	default:
		return val
	}
}
