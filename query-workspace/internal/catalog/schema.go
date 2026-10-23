package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/schema"
)

func (p *Postgres) LatestSchema(ctx context.Context, target string) (output schema.Snapshot, loadErr error) {
	ctx, end := observability.StartSpan(ctx, "postgres.load_schema", trace.SpanKindClient,
		attribute.String("db.system.name", "postgresql"), attribute.String("db.operation.name", "SELECT"))
	defer func() {
		if errors.Is(loadErr, schema.ErrUnavailable) {
			end(nil)
			return
		}
		end(loadErr)
	}()
	var snapshot schema.Snapshot
	err := p.db.QueryRowContext(ctx,
		`SELECT target, schema_text, schema_hash, discovery_method, captured_at
		 FROM schema_snapshots WHERE target = $1 ORDER BY captured_at DESC, id DESC LIMIT 1`,
		target,
	).Scan(&snapshot.Target, &snapshot.Text, &snapshot.Hash, &snapshot.Method, &snapshot.CapturedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return schema.Snapshot{}, schema.ErrUnavailable
	}
	if err != nil {
		return schema.Snapshot{}, fmt.Errorf("loading schema snapshot: %w", err)
	}
	return snapshot, nil
}

func (p *Postgres) SaveSchema(ctx context.Context, snapshot schema.Snapshot) (saveErr error) {
	ctx, end := observability.StartSpan(ctx, "postgres.save_schema", trace.SpanKindClient,
		attribute.String("db.system.name", "postgresql"), attribute.String("db.operation.name", "INSERT"))
	defer func() { end(saveErr) }()
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO schema_snapshots (target, schema_text, schema_hash, discovery_method, captured_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		snapshot.Target, snapshot.Text, snapshot.Hash, snapshot.Method, snapshot.CapturedAt,
	)
	if err != nil {
		return fmt.Errorf("saving schema snapshot: %w", err)
	}
	return nil
}
