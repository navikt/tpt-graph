package neo4j

import (
	"context"
	"fmt"
	"strings"
	"time"

	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"tpt-graph/internal/graphapi"
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

// ModuleSync holds the last sync timestamp for a single Cartography module.
type ModuleSync struct {
	Module   string
	LastSync time.Time
}

// FormattedTime returns the sync time as a readable UTC string, or "unknown" if zero.
func (m ModuleSync) FormattedTime() string {
	if m.LastSync.IsZero() {
		return "unknown"
	}
	return m.LastSync.UTC().Format("2006-01-02 15:04 UTC")
}

// StatusColor returns "green", "yellow", or "red" based on how stale the sync is.
func (m ModuleSync) StatusColor() string {
	if m.LastSync.IsZero() {
		return "red"
	}
	age := time.Since(m.LastSync)
	switch {
	case age < 24*time.Hour:
		return "green"
	case age < 72*time.Hour:
		return "yellow"
	default:
		return "red"
	}
}

// ShortImage returns the image name and tag without the registry/repository prefix.
// e.g. "europe-north1-docker.pkg.dev/nais-management-233d/aap/aap-api:abc1234" → "aap-api:abc1234"
func (d DependencyUsage) ShortImage() string {
	if i := strings.LastIndex(d.RunningImage, "/"); i >= 0 {
		return d.RunningImage[i+1:]
	}
	return d.RunningImage
}

// ShortCommit returns the first 7 characters of the commit SHA.
func (d DependencyUsage) ShortCommit() string {
	if len(d.DeployedCommit) > 7 {
		return d.DeployedCommit[:7]
	}
	return d.DeployedCommit
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

// FindDependencyUsages returns all running apps that depend on the given package.
// version and ecosystem are optional — pass empty string to omit each filter.
func (c *Client) FindDependencyUsages(ctx context.Context, name, version, ecosystem string) ([]DependencyUsage, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx,
		`MATCH (dep:Dependency)
		 WHERE dep.name = $name
		   AND ($version  = '' OR dep.version  = $version)
		   AND ($ecosystem = '' OR dep.ecosystem = $ecosystem)
		 MATCH (dep)<-[:REQUIRES]-(repo:GitHubRepository)
		      <-[:DEPLOYED_FROM]-(d:NaisDeployment)
		      <-[:ACTIVE_DEPLOYMENT]-(app:NaisApp)
		      -[:RUNS_IMAGE]->(container:KubernetesContainer)
		      <-[:RESOURCE]-(cluster:KubernetesCluster)
		 MATCH (ns:KubernetesNamespace)-[:CONTAINS]->(container)
		 RETURN DISTINCT
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

// FindNamespaceByPath returns the distinct namespaces of all ingresses whose
// rules contain the given path fragment.
func (c *Client) FindNamespaceByPath(ctx context.Context, path string) ([]string, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx,
		`MATCH (ing:KubernetesIngress)
		 WHERE ing.rules CONTAINS $path
		 RETURN DISTINCT ing.namespace AS namespace
		 ORDER BY namespace`,
		map[string]any{"path": path},
	)
	if err != nil {
		return nil, fmt.Errorf("path query failed: %w", err)
	}

	var namespaces []string
	for result.Next(ctx) {
		namespaces = append(namespaces, stringField(result.Record(), "namespace"))
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration: %w", err)
	}

	return namespaces, nil
}

// Returns an empty string if the field is absent or not a string.
func stringField(rec *neodriver.Record, key string) string {
	val, ok := rec.Get(key)
	if !ok || val == nil {
		return ""
	}
	s, _ := val.(string)
	return s
}

// FindLastSync returns the most recent lastupdated timestamp per Cartography module.
func (c *Client) FindLastSync(ctx context.Context) ([]ModuleSync, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx,
		`UNWIND [
		   {module: 'nais',       label: 'NaisApp'},
		   {module: 'github',     label: 'GitHubRepository'},
		   {module: 'kubernetes', label: 'KubernetesIngress'}
		 ] AS m
		 CALL {
		   WITH m
		   MATCH (n) WHERE m.label IN labels(n)
		   RETURN max(n.lastupdated) AS ts
		 }
		 RETURN m.module AS module, datetime({epochSeconds: ts}) AS last_sync
		 ORDER BY last_sync DESC`,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("last sync query failed: %w", err)
	}

	var syncs []ModuleSync
	for result.Next(ctx) {
		rec := result.Record()
		module := stringField(rec, "module")

		val, ok := rec.Get("last_sync")
		if !ok || val == nil {
			syncs = append(syncs, ModuleSync{Module: module})
			continue
		}
		// The Neo4j driver returns timezone-aware datetime values as time.Time
		// and LocalDateTime values as neodriver.LocalDateTime.
		var t time.Time
		switch v := val.(type) {
		case time.Time:
			t = v
		case neodriver.LocalDateTime:
			t = v.Time()
		}
		syncs = append(syncs, ModuleSync{Module: module, LastSync: t})
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration: %w", err)
	}

	return syncs, nil
}

// GraphSeed returns the initial subgraph for a GitHub repository name.
func (c *Client) GraphSeed(ctx context.Context, repo string) (*graphapi.GraphPayload, error) {
	return graphapi.GraphSeed(ctx, c.drv, repo)
}

// GraphExpand returns the immediate neighbours of a node by element ID.
func (c *Client) GraphExpand(ctx context.Context, elementID string, knownIDs []string) (*graphapi.GraphPayload, error) {
	return graphapi.GraphExpand(ctx, c.drv, elementID, knownIDs)
}
