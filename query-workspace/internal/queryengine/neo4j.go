package queryengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/workspace"
)

type Neo4jExecutor struct {
	driver   neodriver.DriverWithContext
	database string
	timeout  time.Duration
	maxRows  int
	maxBytes int
}

func NewNeo4jExecutor(uri, user, password, database string, timeout time.Duration, maxRows, maxBytes int) (*Neo4jExecutor, error) {
	if uri == "" || user == "" || password == "" || database == "" {
		return nil, errors.New("Neo4j URI, user, password, and database are required")
	}
	if timeout <= 0 || maxRows <= 0 || maxBytes <= 0 {
		return nil, errors.New("Neo4j query timeout and result limits must be positive")
	}
	driver, err := neodriver.NewDriverWithContext(
		uri,
		neodriver.BasicAuth(user, password, ""),
		func(config *neodriver.Config) {
			config.MaxTransactionRetryTime = 0
		},
	)
	if err != nil {
		return nil, fmt.Errorf("creating Neo4j driver: %w", err)
	}
	return &Neo4jExecutor{
		driver:   driver,
		database: database,
		timeout:  timeout,
		maxRows:  maxRows,
		maxBytes: maxBytes,
	}, nil
}

func (e *Neo4jExecutor) Close(ctx context.Context) error {
	return e.driver.Close(ctx)
}

func (e *Neo4jExecutor) VerifyConnectivity(ctx context.Context) error {
	return e.driver.VerifyConnectivity(ctx)
}

func (e *Neo4jExecutor) Execute(ctx context.Context, query string, parameters map[string]any) (output workspace.QueryResult, executeErr error) {
	ctx, end := observability.StartSpan(ctx, "neo4j.execute", trace.SpanKindClient,
		attribute.String("db.system.name", "neo4j"))
	defer func() { end(executeErr) }()
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	session := e.driver.NewSession(ctx, neodriver.SessionConfig{
		AccessMode:   neodriver.AccessModeRead,
		DatabaseName: e.database,
		FetchSize:    100,
	})
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer closeCancel()
		if err := session.Close(closeCtx); err != nil {
			executeErr = errors.Join(executeErr, fmt.Errorf("closing Neo4j session: %w", err))
		}
	}()

	tx, err := session.BeginTransaction(ctx, neodriver.WithTxTimeout(e.timeout))
	if err != nil {
		return workspace.QueryResult{}, fmt.Errorf("starting Neo4j read transaction: %w", err)
	}
	finished := false
	defer func() {
		if !finished {
			rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer rollbackCancel()
			if err := tx.Rollback(rollbackCtx); err != nil {
				executeErr = errors.Join(executeErr, fmt.Errorf("rolling back Neo4j read transaction: %w", err))
			}
		}
	}()

	result, err := tx.Run(ctx, query, parameters)
	if err != nil {
		return workspace.QueryResult{}, fmt.Errorf("running Neo4j query: %w", err)
	}
	columns, err := result.Keys()
	if err != nil {
		return workspace.QueryResult{}, fmt.Errorf("reading Neo4j result columns: %w", err)
	}
	collector := newRowCollector(columns, e.maxRows, e.maxBytes)
	for result.Next(ctx) {
		if !collector.add(result.Record().Values) {
			break
		}
	}
	if err := result.Err(); err != nil {
		return collector.result(), fmt.Errorf("reading Neo4j results: %w", err)
	}
	if collector.truncated {
		rollbackCtx, rollbackCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer rollbackCancel()
		if err := tx.Rollback(rollbackCtx); err != nil {
			return collector.result(), fmt.Errorf("canceling truncated Neo4j query: %w", err)
		}
		finished = true
		return collector.result(), nil
	}

	summary, err := result.Consume(ctx)
	if err != nil {
		return collector.result(), fmt.Errorf("consuming Neo4j result: %w", err)
	}
	if summary.StatementType() != neodriver.StatementTypeReadOnly {
		return collector.result(), errors.New("Neo4j classified the query as non-read-only")
	}
	if err := tx.Commit(ctx); err != nil {
		return collector.result(), fmt.Errorf("committing Neo4j read transaction: %w", err)
	}
	finished = true
	return collector.result(), nil
}

type rowCollector struct {
	columns   []string
	rows      [][]string
	maxRows   int
	maxBytes  int
	bytesUsed int
	truncated bool
	graph     workspace.ResultGraph
	seen      map[string]bool
}

func newRowCollector(columns []string, maxRows, maxBytes int) *rowCollector {
	return &rowCollector{
		columns:  columns,
		rows:     make([][]string, 0, min(maxRows, 32)),
		maxRows:  maxRows,
		maxBytes: maxBytes,
		seen:     make(map[string]bool),
	}
}

// add keeps a row and the graph entities it introduces only if both fit the output budget,
// so truncation never leaves graph entities without the row that returned them.
func (c *rowCollector) add(values []any) bool {
	if len(c.rows) >= c.maxRows {
		c.truncated = true
		return false
	}
	entities := rowEntities{seen: c.seen, pending: make(map[string]bool)}
	row := make([]string, len(values))
	for i, value := range values {
		row[i] = entities.cell(value)
	}
	size, err := encodedSize(row)
	if err == nil && (len(entities.nodes) > 0 || len(entities.relationships) > 0) {
		var graphSize int
		graphSize, err = encodedSize(entities.nodes, entities.relationships, entities.rowNodes)
		size += graphSize
	}
	if err != nil || c.bytesUsed+size > c.maxBytes {
		c.truncated = true
		return false
	}
	c.bytesUsed += size
	c.rows = append(c.rows, row)
	c.graph.Nodes = append(c.graph.Nodes, entities.nodes...)
	c.graph.Relationships = append(c.graph.Relationships, entities.relationships...)
	c.graph.RowNodes = append(c.graph.RowNodes, entities.rowNodes)
	for id := range entities.pending {
		c.seen[id] = true
	}
	return true
}

func encodedSize(values ...any) (int, error) {
	size := 0
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, err
		}
		size += len(encoded)
	}
	return size, nil
}

func (c *rowCollector) result() workspace.QueryResult {
	result := workspace.QueryResult{
		Columns:   c.columns,
		Rows:      c.rows,
		Truncated: c.truncated,
	}
	if len(c.graph.Nodes) > 0 || len(c.graph.Relationships) > 0 {
		result.Graph = c.graph
	}
	return result
}

// rowEntities collects the graph entities in one row that have not been collected before.
type rowEntities struct {
	seen, pending map[string]bool
	nodes         []workspace.GraphNode
	relationships []workspace.GraphRelationship
	rowNodes      []string
}

func (e *rowEntities) cell(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(value)
	case dbtype.Node, dbtype.Relationship, dbtype.Path:
		return e.display(value).(string)
	}
	return marshalDisplay(e.display(value))
}

// display records graph entities in value and replaces them with readable text.
func (e *rowEntities) display(value any) any {
	switch value := value.(type) {
	case dbtype.Node:
		e.node(value)
		return nodeDisplay(value)
	case dbtype.Relationship:
		e.relationship(value)
		return "[:" + value.Type + "]"
	case dbtype.Path:
		var text strings.Builder
		for i, node := range value.Nodes {
			e.node(node)
			if i > 0 {
				relationship := value.Relationships[i-1]
				e.relationship(relationship)
				if relationship.StartElementId == node.ElementId {
					text.WriteString(" <-[:" + relationship.Type + "]- ")
				} else {
					text.WriteString(" -[:" + relationship.Type + "]-> ")
				}
			}
			text.WriteString(nodeDisplay(node))
		}
		return text.String()
	case []any:
		items := make([]any, len(value))
		for i, item := range value {
			items[i] = e.display(item)
		}
		return items
	case map[string]any:
		items := make(map[string]any, len(value))
		for key, item := range value {
			items[key] = e.display(item)
		}
		return items
	}
	return value
}

func (e *rowEntities) node(node dbtype.Node) {
	e.rowNodes = append(e.rowNodes, node.ElementId)
	if e.claim(node.ElementId) {
		e.nodes = append(e.nodes, workspace.GraphNode{
			ID: node.ElementId, Labels: node.Labels, Properties: displayProperties(node.Props),
		})
	}
}

func (e *rowEntities) relationship(relationship dbtype.Relationship) {
	if e.claim(relationship.ElementId) {
		e.relationships = append(e.relationships, workspace.GraphRelationship{
			ID: relationship.ElementId, Type: relationship.Type,
			StartID: relationship.StartElementId, EndID: relationship.EndElementId,
			Properties: displayProperties(relationship.Props),
		})
	}
}

func (e *rowEntities) claim(id string) bool {
	if e.seen[id] || e.pending[id] {
		return false
	}
	e.pending[id] = true
	return true
}

func displayProperties(properties map[string]any) map[string]string {
	displayed := make(map[string]string, len(properties))
	for key, value := range properties {
		displayed[key] = displayValue(value)
	}
	return displayed
}

// nodeDisplay renders a node as "(:Label) name", preferring the most human-readable property.
func nodeDisplay(node dbtype.Node) string {
	label := ""
	if len(node.Labels) > 0 {
		label = ":" + node.Labels[0]
	}
	return "(" + label + ") " + workspace.NodeCaption(node.ElementId, displayProperties(node.Props))
}

func displayValue(value any) string {
	if value == nil {
		return ""
	}
	switch value.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(value)
	}
	return marshalDisplay(value)
}

func marshalDisplay(value any) string {
	encoded, err := json.Marshal(value)
	if err == nil {
		return string(encoded)
	}
	return fmt.Sprint(value)
}
