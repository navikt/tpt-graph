package neo4j

import (
	"context"
	"fmt"
	"regexp"
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
	Version        string
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

// IngressMatch represents a resolved ingress ownership result: the
// Kubernetes namespace the ingress lives in, plus the Nais workloads whose
// running containers are actually routed to by that ingress (traversed via
// KubernetesIngress -[:TARGETS]-> KubernetesService -[:TARGETS]-> KubernetesPod
// <-[:WORKLOAD_PARENT]- KubernetesContainer <-[:RUNS_IMAGE]- NaisApp).
// Workloads is empty when cartography hasn't resolved that chain for the
// ingress (e.g. a non-Nais-managed ingress).
type IngressMatch struct {
	Namespace string
	Workloads []string
}

// findNamespaceByIngressQuery is the Cypher query used by FindNamespaceByIngress.
// Exposed as a variable so tests can assert its content.
var findNamespaceByIngressQuery = `
	MATCH (ns:KubernetesNamespace)-[:CONTAINS]->(ing:KubernetesIngress)
	WHERE ANY(host IN ing.host_names WHERE host CONTAINS $hostname)
	WITH ns, ing LIMIT 1
	OPTIONAL MATCH (ing)-[:TARGETS]->(:KubernetesService)-[:TARGETS]->(:KubernetesPod)
	              <-[:WORKLOAD_PARENT]-(:KubernetesContainer)<-[:RUNS_IMAGE]-(app:NaisApp)
	RETURN ns.name AS namespace, collect(DISTINCT app.name) AS workloads`

// FindNamespaceByIngress returns the Kubernetes namespace and Nais workloads that
// own the given ingress hostname. hostname is matched with a CONTAINS check
// against the stored host_names list. Returns a zero-value IngressMatch when
// the query succeeds but no namespace is found.
func (c *Client) FindNamespaceByIngress(ctx context.Context, hostname string) (IngressMatch, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, findNamespaceByIngressQuery,
		map[string]any{"hostname": hostname},
	)
	if err != nil {
		return IngressMatch{}, fmt.Errorf("query failed: %w", err)
	}

	if result.Next(ctx) {
		rec := result.Record()
		return IngressMatch{
			Namespace: stringField(rec, "namespace"),
			Workloads: stringSliceField(rec, "workloads"),
		}, nil
	}

	if err := result.Err(); err != nil {
		return IngressMatch{}, fmt.Errorf("result iteration: %w", err)
	}

	return IngressMatch{}, nil
}

// findDependencyUsagesQuery is the Cypher query used by FindDependencyUsages.
// Exposed as a variable so tests can assert its content.
var findDependencyUsagesQuery = `MATCH (dep:Dependency)
	 WHERE dep.name = $name
	   AND ($version  = '' OR dep.version  =~ $version)
	   AND ($ecosystem = '' OR dep.ecosystem = $ecosystem)
	 MATCH (dep)<-[:REQUIRES]-(repo:GitHubRepository)
	      <-[:DEPLOYED_FROM]-(d:NaisDeployment {is_active: true})
	      <-[:ACTIVE_DEPLOYMENT]-(app:NaisApp)
	      -[:RUNS_IMAGE]->(container:KubernetesContainer)
	      <-[:RESOURCE]-(cluster:KubernetesCluster)
	 MATCH (ns:KubernetesNamespace)-[:CONTAINS]->(container)
	 RETURN DISTINCT
	   cluster.name    AS cluster,
	   ns.name         AS namespace,
	   app.name        AS app,
	   dep.version     AS dep_version,
	   container.image AS running_image,
	   d.commit_sha    AS deployed_commit,
	   d.created_at    AS deployed_at
	 ORDER BY cluster.name, ns.name, app.name`

// versionToRegex converts a user-supplied version string to a Neo4j-compatible
// regex pattern. An empty string is returned unchanged (the query treats it as
// "match all"). A plain version like "1.2.3" is anchored to an exact match.
// Glob-style wildcards (*) are expanded to ".*", allowing patterns such as
// "4.*" to match any 4.x release across an entire ecosystem.
func versionToRegex(v string) string {
	if v == "" {
		return v
	}
	// Split on '*', escape all other regex metacharacters in each segment,
	// then rejoin with '.*'.
	parts := strings.Split(v, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return "^" + strings.Join(parts, ".*") + "$"
}

// FindDependencyUsages returns all running apps that depend on the given package.
// version and ecosystem are optional — pass empty string to omit each filter.
// version supports glob-style wildcards: e.g. "4.*" matches any 4.x release.
func (c *Client) FindDependencyUsages(ctx context.Context, name, version, ecosystem string) ([]DependencyUsage, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, findDependencyUsagesQuery,
		map[string]any{"name": name, "version": versionToRegex(version), "ecosystem": ecosystem},
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
			Version:        stringField(rec, "dep_version"),
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

// findNamespaceByPathQuery is the Cypher query used by FindNamespaceByPath.
// Exposed as a variable so tests can assert its content.
var findNamespaceByPathQuery = `
	MATCH (ing:KubernetesIngress)
	WHERE ing.rules CONTAINS $path
	OPTIONAL MATCH (ing)-[:TARGETS]->(:KubernetesService)-[:TARGETS]->(:KubernetesPod)
	              <-[:WORKLOAD_PARENT]-(:KubernetesContainer)<-[:RUNS_IMAGE]-(app:NaisApp)
	RETURN ing.namespace AS namespace, collect(DISTINCT app.name) AS workloads
	ORDER BY namespace`

// FindNamespaceByPath returns, for each distinct namespace whose ingresses have
// rules containing the given path fragment, the namespace and the Nais
// workloads routed to by those ingresses.
func (c *Client) FindNamespaceByPath(ctx context.Context, path string) ([]IngressMatch, error) {
	session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
	defer session.Close(ctx)

	result, err := session.Run(ctx, findNamespaceByPathQuery,
		map[string]any{"path": path},
	)
	if err != nil {
		return nil, fmt.Errorf("path query failed: %w", err)
	}

	var matches []IngressMatch
	for result.Next(ctx) {
		rec := result.Record()
		matches = append(matches, IngressMatch{
			Namespace: stringField(rec, "namespace"),
			Workloads: stringSliceField(rec, "workloads"),
		})
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("result iteration: %w", err)
	}

	return matches, nil
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

// stringSliceField returns a []string for a collect()-produced field, skipping
// any non-string or empty entries (e.g. the null collected when an OPTIONAL
// MATCH found no NaisApp).
func stringSliceField(rec *neodriver.Record, key string) []string {
	val, ok := rec.Get(key)
	if !ok || val == nil {
		return nil
	}
	raw, ok := val.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// FindLastSync returns the most recent lastupdated timestamp per Cartography module.
func (c *Client) FindLastSync(ctx context.Context) ([]ModuleSync, error) {
	modules := []struct {
		module string
		label  string
	}{
		{"nais", "NaisApp"},
		{"github", "GitHubRepository"},
		{"kubernetes", "KubernetesIngress"},
	}

	var syncs []ModuleSync
	for _, m := range modules {
		session := c.drv.NewSession(ctx, neodriver.SessionConfig{AccessMode: neodriver.AccessModeRead})
		result, err := session.Run(ctx,
			`MATCH (n:`+m.label+`) RETURN max(n.lastupdated) AS ts`,
			nil,
		)
		if err != nil {
			_ = session.Close(ctx)
			return nil, fmt.Errorf("last sync query for %s: %w", m.module, err)
		}

		var t time.Time
		if result.Next(ctx) {
			val, ok := result.Record().Get("ts")
			if ok && val != nil {
				switch v := val.(type) {
				case int64:
					t = time.Unix(v, 0)
				case float64:
					t = time.Unix(int64(v), 0)
				case time.Time:
					t = v
				case neodriver.LocalDateTime:
					t = v.Time()
				}
			}
		}
		_ = result.Err()
		_ = session.Close(ctx)
		syncs = append(syncs, ModuleSync{Module: m.module, LastSync: t})
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
