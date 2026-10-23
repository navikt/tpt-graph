package queryengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
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
}

func newRowCollector(columns []string, maxRows, maxBytes int) *rowCollector {
	return &rowCollector{
		columns:  columns,
		rows:     make([][]string, 0, min(maxRows, 32)),
		maxRows:  maxRows,
		maxBytes: maxBytes,
	}
}

func (c *rowCollector) add(values []any) bool {
	if len(c.rows) >= c.maxRows {
		c.truncated = true
		return false
	}
	row := make([]string, len(values))
	for i, value := range values {
		row[i] = displayValue(value)
	}
	encoded, err := json.Marshal(row)
	if err != nil {
		c.truncated = true
		return false
	}
	if c.bytesUsed+len(encoded) > c.maxBytes {
		c.truncated = true
		return false
	}
	c.bytesUsed += len(encoded)
	c.rows = append(c.rows, row)
	return true
}

func (c *rowCollector) result() workspace.QueryResult {
	return workspace.QueryResult{
		Columns:   c.columns,
		Rows:      c.rows,
		Truncated: c.truncated,
	}
}

func displayValue(value any) string {
	if value == nil {
		return ""
	}
	switch value.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return fmt.Sprint(value)
	}
	encoded, err := json.Marshal(value)
	if err == nil {
		return string(encoded)
	}
	return fmt.Sprint(value)
}
