package neo4j

import (
	"context"
	"fmt"

	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// DependencyUsage is a single row returned by FindDependencyUsages.
type DependencyUsage struct {
	Cluster        string
	Namespace      string
	App            string
	RunningImage   string
	DeployedCommit string
	DeployedAt     string
}

// Client wraps the Neo4j driver with application-level query methods.
type Client struct {
	drv neodriver.DriverWithContext
}

// NewClient creates a new Client and initialises the underlying Neo4j driver.
// The driver is lazy — no connection is made until the first query or VerifyConnectivity.
func NewClient(uri, user, password string) (*Client, error) {
	drv, err := neodriver.NewDriverWithContext(uri, neodriver.BasicAuth(user, password, ""))
	if err != nil {
		return nil, fmt.Errorf("create neo4j driver: %w", err)
	}
	return &Client{drv: drv}, nil
}

// Close releases the driver's connection pool. Should be called on shutdown.
func (c *Client) Close(ctx context.Context) {
	_ = c.drv.Close(ctx)
}

// VerifyConnectivity confirms that the driver can reach the database.
func (c *Client) VerifyConnectivity(ctx context.Context) error {
	return c.drv.VerifyConnectivity(ctx)
}

// FindNamespaceByIngress returns the Kubernetes namespace that owns the given ingress hostname.
// hostname is matched with a CONTAINS check against the stored host_names list.
// Returns ("", nil) when the query succeeds but no namespace is found.
func (c *Client) FindNamespaceByIngress(ctx context.Context, hostname string) (string, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx,
		`MATCH (ns:KubernetesNamespace)-[:CONTAINS]->(ing:KubernetesIngress)
		 WHERE ANY(host IN ing.host_names WHERE host CONTAINS $hostname)
		 RETURN ns.name AS namespace LIMIT 1`,
		map[string]any{"hostname": hostname},
	)
	if err != nil {
		return "", fmt.Errorf("query failed: %w", err)
	}

	if result.Next(ctx) {
		val, ok := result.Record().Get("namespace")
		if !ok {
			return "", nil
		}
		name, ok := val.(string)
		if !ok {
			return "", nil
		}
		return name, nil
	}

	if err := result.Err(); err != nil {
		return "", fmt.Errorf("result iteration: %w", err)
	}

	return "", nil
}

// FindDependencyUsages returns all running apps that depend on the given package name,
// version, and ecosystem.
func (c *Client) FindDependencyUsages(ctx context.Context, name, version, ecosystem string) ([]DependencyUsage, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx,
		`MATCH (dep:Dependency {name: $name, version: $version, ecosystem: $ecosystem})
		      <-[:REQUIRES]-(repo:GitHubRepository)
		      <-[:DEPLOYED_FROM]-(d:NaisDeployment)
		      <-[:ACTIVE_DEPLOYMENT]-(app:NaisApp)
		      -[:RUNS_IMAGE]->(container:KubernetesContainer)
		      <-[:RESOURCE]-(cluster:KubernetesCluster)
		 MATCH (ns:KubernetesNamespace)-[:CONTAINS]->(container)
		 RETURN
		   cluster.name    AS cluster,
		   ns.name         AS namespace,
		   app.name        AS app,
		   container.image AS running_image,
		   d.commit_sha    AS deployed_commit,
		   d.created_at    AS deployed_at
		 ORDER BY cluster.name, ns.name, app.name`,
		map[string]any{"name": name, "version": version, "ecosystem": ecosystem},
	)
	if err != nil {
		return nil, fmt.Errorf("dependency query failed: %w", err)
	}

	var usages []DependencyUsage
	for result.Next(ctx) {
		rec := result.Record()
		usages = append(usages, DependencyUsage{
			Cluster:        stringField(rec, "cluster"),
			Namespace:      stringField(rec, "namespace"),
			App:            stringField(rec, "app"),
			RunningImage:   stringField(rec, "running_image"),
			DeployedCommit: stringField(rec, "deployed_commit"),
			DeployedAt:     stringField(rec, "deployed_at"),
		})
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration: %w", err)
	}

	return usages, nil
}

// stringField safely extracts a string value from a record field.
// Returns an empty string if the field is absent or not a string.
func stringField(rec *neodriver.Record, key string) string {
	val, ok := rec.Get(key)
	if !ok || val == nil {
		return ""
	}
	s, _ := val.(string)
	return s
}
