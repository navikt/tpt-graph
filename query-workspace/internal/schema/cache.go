package schema

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

const MaxBytes = 512_000

var ErrUnavailable = errors.New("no cached schema is available yet")

type Snapshot struct {
	Target     string
	Text       string
	Hash       string
	Method     string
	CapturedAt time.Time
}

type Source interface {
	Discover(context.Context) (string, error)
}

type Store interface {
	LatestSchema(context.Context, string) (Snapshot, error)
	SaveSchema(context.Context, Snapshot) error
}

type Cache struct {
	source        Source
	store         Store
	target        string
	location      *time.Location
	logger        *slog.Logger
	refreshFailed atomic.Bool
}

func NewCache(source Source, store Store, target string, location *time.Location, logger *slog.Logger) *Cache {
	return &Cache{source: source, store: store, target: target, location: location, logger: logger}
}

func (c *Cache) Current(ctx context.Context) (Snapshot, error) {
	return c.store.LatestSchema(ctx, c.target)
}

func (c *Cache) Refresh(ctx context.Context) (refreshErr error) {
	defer func() {
		c.refreshFailed.Store(refreshErr != nil)
	}()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	text, err := c.source.Discover(ctx)
	if err != nil {
		return fmt.Errorf("discovering Neo4j schema: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("discovered Neo4j schema is empty")
	}
	if len(text) > MaxBytes {
		return fmt.Errorf("discovered Neo4j schema exceeds %d bytes", MaxBytes)
	}
	hash := sha256.Sum256([]byte(text))
	snapshot := Snapshot{
		Target:     c.target,
		Text:       text,
		Hash:       fmt.Sprintf("%x", hash),
		Method:     "neo4j-native-v1",
		CapturedAt: time.Now().UTC(),
	}
	if err := c.store.SaveSchema(ctx, snapshot); err != nil {
		return fmt.Errorf("persisting schema snapshot: %w", err)
	}
	c.logger.Info("schema snapshot refreshed", "schema_sha256", snapshot.Hash,
		"captured_at", snapshot.CapturedAt, "method", snapshot.Method, "bytes", len(text))
	return nil
}

func nextMidnight(now time.Time) time.Time {
	return time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
}

func (c *Cache) RefreshIfDue(ctx context.Context, now time.Time) error {
	now = now.In(c.location)
	snapshot, err := c.Current(ctx)
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return fmt.Errorf("loading cached schema before refresh: %w", err)
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if err == nil && !snapshot.CapturedAt.Before(midnight) {
		return nil
	}
	return c.Refresh(ctx)
}

type Status struct {
	Available     bool
	CapturedAt    string
	Age           string
	Stale         bool
	RefreshFailed bool
}

func (c *Cache) Status(ctx context.Context) (Status, error) {
	status := Status{RefreshFailed: c.refreshFailed.Load()}
	snapshot, err := c.Current(ctx)
	if errors.Is(err, ErrUnavailable) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	now := time.Now().In(c.location)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.location)
	status.Available = true
	status.CapturedAt = snapshot.CapturedAt.In(c.location).Format("2006-01-02 15:04:05 MST")
	status.Age = now.Sub(snapshot.CapturedAt).Truncate(time.Second).String()
	status.Stale = snapshot.CapturedAt.Before(midnight)
	return status, nil
}

func (c *Cache) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.RefreshIfDue(ctx, time.Now()); err != nil {
			c.refreshFailed.Store(true)
			c.logger.Error("schema refresh failed; retaining last successful snapshot", "error", err)
		}
		next := nextMidnight(time.Now().In(c.location))
		c.logger.Info("schema refresh scheduled", "next_refresh", next, "timezone", c.location.String())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
