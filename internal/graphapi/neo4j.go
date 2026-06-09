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

// GraphSeed runs the shallow seed query starting from a GitHubRepository by name.
func GraphSeed(ctx context.Context, drv neodriver.DriverWithContext, repo string) (*GraphPayload, error) {
	session := drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, `
		MATCH (repo:GitHubRepository {name: $repo})
		OPTIONAL MATCH (repo)<-[:DEPLOYED_FROM]-(d:NaisDeployment)
		              <-[:HAS_DEPLOYMENT]-(app:NaisApp)
		OPTIONAL MATCH (app)-[:RUNS_IN]->(ns:KubernetesNamespace)
		OPTIONAL MATCH (app)<-[:HAS_APP]-(team:NaisTeam)
		RETURN repo, d, app, ns, team,
		       [(repo)<-[r1:DEPLOYED_FROM]-(d) WHERE d IS NOT NULL | r1] +
		       [(d)<-[r2:HAS_DEPLOYMENT]-(app) WHERE app IS NOT NULL | r2] +
		       [(app)-[r3:RUNS_IN]->(ns) WHERE ns IS NOT NULL | r3] +
		       [(app)<-[r4:HAS_APP]-(team) WHERE team IS NOT NULL | r4] AS rels`,
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
		if val, ok := rec.Get("rels"); ok && val != nil {
			if relList, ok := val.([]any); ok {
				for _, r := range relList {
					if rel, ok := r.(neo4j.Relationship); ok {
						rels[rel.ElementId] = rel
					}
				}
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
	degrees, err := batchDegrees(ctx, drv, nodeIDs)
	if err != nil {
		return nil, err
	}

	return buildPayload(nodes, rels, degrees), nil
}

// GraphExpand returns all immediate neighbours of a node, excluding already-known nodes.
func GraphExpand(ctx context.Context, drv neodriver.DriverWithContext, elementID string, knownIDs []string) (*GraphPayload, error) {
	session := drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, `
		MATCH (n) WHERE elementId(n) = $id
		MATCH (n)-[r]-(neighbour)
		RETURN r, neighbour`,
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
				rels[rel.ElementId] = rel
			}
		}
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("graph expand iteration: %w", err)
	}

	// Filter out nodes the client already has.
	for id := range nodes {
		if known[id] {
			delete(nodes, id)
		}
	}

	nodeIDs := make([]string, 0, len(nodes))
	for id := range nodes {
		nodeIDs = append(nodeIDs, id)
	}
	degrees, err := batchDegrees(ctx, drv, nodeIDs)
	if err != nil {
		return nil, err
	}

	return buildPayload(nodes, rels, degrees), nil
}

// batchDegrees returns the relationship count for each node in a single query.
func batchDegrees(ctx context.Context, drv neodriver.DriverWithContext, ids []string) (map[string]int64, error) {
	if len(ids) == 0 {
		return map[string]int64{}, nil
	}
	session := drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, `
		UNWIND $ids AS eid
		MATCH (n) WHERE elementId(n) = eid
		RETURN elementId(n) AS id, count{ (n)-[]-() } AS degree`,
		map[string]any{"ids": ids},
	)
	if err != nil {
		return nil, fmt.Errorf("degree query: %w", err)
	}

	degrees := make(map[string]int64, len(ids))
	for result.Next(ctx) {
		rec := result.Record()
		id, _ := rec.Get("id")
		deg, _ := rec.Get("degree")
		if idStr, ok := id.(string); ok {
			if degInt, ok := deg.(int64); ok {
				degrees[idStr] = degInt
			}
		}
	}
	return degrees, result.Err()
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
