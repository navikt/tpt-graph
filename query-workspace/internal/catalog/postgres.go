package catalog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"tpt-graph/query-workspace/internal/observability"
	"tpt-graph/query-workspace/internal/queryparams"
	"tpt-graph/query-workspace/internal/workspace"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Postgres struct {
	db *sql.DB
}

func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("opening PostgreSQL connection: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxIdleTime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to PostgreSQL: %w", err)
	}
	if err := goose.SetDialect("postgres"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configuring PostgreSQL migrations: %w", err)
	}
	goose.SetBaseFS(migrations)
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running PostgreSQL migrations: %w", err)
	}
	return &Postgres{db: db}, nil
}

func (p *Postgres) Close() error {
	return p.db.Close()
}

func (p *Postgres) Ping(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

func (p *Postgres) Save(ctx context.Context, query workspace.SavedQuery) (saved workspace.SavedQuery, saveErr error) {
	ctx, end := observability.StartSpan(ctx, "postgres.save_query", trace.SpanKindClient,
		attribute.String("db.system.name", "postgresql"), attribute.String("db.operation.name", "INSERT"))
	defer func() { end(saveErr) }()
	id, err := newID()
	if err != nil {
		return workspace.SavedQuery{}, fmt.Errorf("creating saved query ID: %w", err)
	}
	parameters, err := json.Marshal(query.Parameters)
	if err != nil {
		return workspace.SavedQuery{}, fmt.Errorf("encoding saved query parameters: %w", err)
	}
	err = p.db.QueryRowContext(
		ctx,
		`INSERT INTO saved_queries (id, name, category, question, cypher, parameters)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING created_at`,
		id, query.Name, query.Category, query.Question, query.Cypher, string(parameters),
	).Scan(&query.CreatedAt)
	if err != nil {
		return workspace.SavedQuery{}, fmt.Errorf("saving query: %w", err)
	}
	query.ID = id
	return query, nil
}

func (p *Postgres) Get(ctx context.Context, id string) (saved workspace.SavedQuery, getErr error) {
	ctx, end := observability.StartSpan(ctx, "postgres.get_query", trace.SpanKindClient,
		attribute.String("db.system.name", "postgresql"), attribute.String("db.operation.name", "SELECT"))
	defer func() { end(getErr) }()
	query, err := scanQuery(p.db.QueryRowContext(
		ctx,
		`SELECT id, name, category, question, cypher, parameters, created_at
		 FROM saved_queries WHERE id = $1`,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return workspace.SavedQuery{}, workspace.ErrNotFound
	}
	if err != nil {
		return workspace.SavedQuery{}, fmt.Errorf("loading saved query: %w", err)
	}
	return query, nil
}

func (p *Postgres) List(ctx context.Context) (saved []workspace.SavedQuery, listErr error) {
	ctx, end := observability.StartSpan(ctx, "postgres.list_queries", trace.SpanKindClient,
		attribute.String("db.system.name", "postgresql"), attribute.String("db.operation.name", "SELECT"))
	defer func() { end(listErr) }()
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, name, category, question, cypher, parameters, created_at
		 FROM saved_queries ORDER BY created_at DESC, id`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing saved queries: %w", err)
	}
	defer rows.Close()

	queries := make([]workspace.SavedQuery, 0)
	for rows.Next() {
		query, err := scanQuery(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning saved query: %w", err)
		}
		queries = append(queries, query)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating saved queries: %w", err)
	}
	return queries, nil
}

type rowScanner interface {
	Scan(...any) error
}

func scanQuery(row rowScanner) (workspace.SavedQuery, error) {
	var query workspace.SavedQuery
	var parameters []byte
	if err := row.Scan(&query.ID, &query.Name, &query.Category, &query.Question, &query.Cypher, &parameters, &query.CreatedAt); err != nil {
		return workspace.SavedQuery{}, err
	}
	var err error
	if query.Parameters, err = queryparams.Decode(parameters); err != nil {
		return workspace.SavedQuery{}, fmt.Errorf("decoding saved query parameters: %w", err)
	}
	return query, nil
}

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
