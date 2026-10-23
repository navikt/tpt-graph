package schema

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type testSource struct {
	mu    sync.Mutex
	text  string
	err   error
	calls int
}

func (s *testSource) Discover(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if _, ok := ctx.Deadline(); !ok {
		return "", errors.New("discovery has no deadline")
	}
	return s.text, s.err
}

func (s *testSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *testSource) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

type testStore struct {
	mu        sync.Mutex
	snapshots []Snapshot
	saveErr   error
}

func (s *testStore) LatestSchema(_ context.Context, target string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.snapshots) - 1; i >= 0; i-- {
		if s.snapshots[i].Target == target {
			return s.snapshots[i], nil
		}
	}
	return Snapshot{}, ErrUnavailable
}

func (s *testStore) SaveSchema(_ context.Context, snapshot Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.snapshots = append(s.snapshots, snapshot)
	return nil
}

func TestRefreshPersistsSchemaAndRequestsOnlyReadCache(t *testing.T) {
	source := &testSource{text: "Node properties:\nDependency {name: String}"}
	store := &testStore{}
	cache := NewCache(source, store, "target-database", time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		snapshot, err := cache.Current(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Text != source.text || snapshot.CapturedAt.IsZero() || snapshot.Hash == "" {
			t.Fatalf("invalid cached snapshot: %#v", snapshot)
		}
	}
	if source.calls != 1 || len(store.snapshots) != 1 {
		t.Fatalf("discovery calls = %d, saved snapshots = %d", source.calls, len(store.snapshots))
	}
	if _, err := store.LatestSchema(context.Background(), "different-database"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cache leaked across targets: %v", err)
	}
}

func TestRefreshKeepsPreviousSnapshotOnFailure(t *testing.T) {
	for _, failure := range []string{"discovery", "empty", "oversized", "persistence"} {
		t.Run(failure, func(t *testing.T) {
			source := &testSource{text: "Dependency {name: String}"}
			store := &testStore{}
			cache := NewCache(source, store, "target", time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := cache.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			previous, err := cache.Current(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			switch failure {
			case "discovery":
				source.err = context.DeadlineExceeded
			case "empty":
				source.text = " \n "
			case "oversized":
				source.text = strings.Repeat("x", MaxBytes+1)
			case "persistence":
				source.text = "Changed {name: String}"
				store.saveErr = errors.New("PostgreSQL unavailable")
			}

			if err := cache.Refresh(context.Background()); err == nil {
				t.Fatal("failed refresh was accepted")
			}
			current, err := cache.Current(context.Background())
			if err != nil || current != previous {
				t.Fatalf("previous snapshot lost: %#v, %v", current, err)
			}
		})
	}
}

func TestNextMidnightFollowsOsloCalendarAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		now  time.Time
		wait time.Duration
	}{
		{"ordinary day", time.Date(2026, 10, 23, 11, 20, 0, 0, location), 12*time.Hour + 40*time.Minute},
		{"spring clock change", time.Date(2026, 3, 29, 0, 0, 0, 0, location), 23 * time.Hour},
		{"autumn clock change", time.Date(2026, 10, 25, 0, 0, 0, 0, location), 25 * time.Hour},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := nextMidnight(test.now)
			if next.Sub(test.now) != test.wait || next.Hour() != 0 || next.Minute() != 0 {
				t.Fatalf("next midnight = %v, wait = %v, want %v", next, next.Sub(test.now), test.wait)
			}
		})
	}
}

func TestStartupRefreshesMissingOrOverdueCacheOnly(t *testing.T) {
	location, err := time.LoadLocation("Europe/Oslo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 23, 11, 20, 0, 0, location)
	for _, test := range []struct {
		name     string
		captured time.Time
		wantCall int
	}{
		{"missing", time.Time{}, 1},
		{"missed midnight", now.Add(-12 * time.Hour), 1},
		{"already refreshed today", now.Add(-time.Hour), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := &testSource{text: "Dependency {name: String}"}
			store := &testStore{}
			if !test.captured.IsZero() {
				store.snapshots = []Snapshot{{Target: "target", Text: source.text, CapturedAt: test.captured}}
			}
			cache := NewCache(source, store, "target", location, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err := cache.RefreshIfDue(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if source.calls != test.wantCall {
				t.Fatalf("discovery calls = %d, want %d", source.calls, test.wantCall)
			}
		})
	}
}

func TestStatusShowsUnavailableStaleAndFailedRefresh(t *testing.T) {
	source := &testSource{text: "Dependency {name: String}"}
	store := &testStore{}
	cache := NewCache(source, store, "target", time.UTC, slog.New(slog.NewTextHandler(io.Discard, nil)))
	status, err := cache.Status(context.Background())
	if err != nil || status.Available {
		t.Fatalf("empty cache status = %#v, %v", status, err)
	}
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.snapshots[0].CapturedAt = time.Now().Add(-48 * time.Hour)
	source.err = context.DeadlineExceeded
	if err := cache.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh failure")
	}
	status, err = cache.Status(context.Background())
	if err != nil || !status.Available || !status.Stale || !status.RefreshFailed || status.Age == "" {
		t.Fatalf("failed refresh status = %#v, %v", status, err)
	}
	source.err = nil
	if err := cache.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = cache.Status(context.Background())
	if err != nil || !status.Available || status.Stale || status.RefreshFailed {
		t.Fatalf("recovered status = %#v, %v", status, err)
	}
}

func TestRunRefreshesAtMidnightAndStopsOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		location, err := time.LoadLocation("Europe/Oslo")
		if err != nil {
			t.Fatal(err)
		}
		source := &testSource{text: "Dependency {name: String}"}
		cache := NewCache(source, &testStore{}, "target", location, slog.New(slog.NewTextHandler(io.Discard, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan struct{})
		go func() {
			cache.Run(ctx)
			close(done)
		}()
		synctest.Wait()
		if source.callCount() != 1 {
			t.Fatalf("startup discovery calls = %d, want 1", source.callCount())
		}
		midnight := nextMidnight(time.Now().In(location))
		time.Sleep(time.Until(midnight) - time.Second)
		synctest.Wait()
		if source.callCount() != 1 {
			t.Fatal("refreshed before local midnight")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if source.callCount() != 2 {
			t.Fatalf("discovery calls at midnight = %d, want 2", source.callCount())
		}
		previous, err := cache.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		source.setError(errors.New("schema procedure unavailable"))
		time.Sleep(time.Until(nextMidnight(time.Now().In(location))))
		synctest.Wait()
		current, err := cache.Current(ctx)
		if err != nil || current != previous {
			t.Fatalf("failed scheduled refresh replaced cache: %#v, %v", current, err)
		}
		status, err := cache.Status(ctx)
		if err != nil || !status.Stale || !status.RefreshFailed {
			t.Fatalf("scheduled failure not visible: %#v, %v", status, err)
		}
		source.setError(nil)
		time.Sleep(time.Until(nextMidnight(time.Now().In(location))))
		synctest.Wait()
		status, err = cache.Status(ctx)
		if err != nil || status.Stale || status.RefreshFailed || source.callCount() != 4 {
			t.Fatalf("next midnight did not recover: %#v, %v, calls %d", status, err, source.callCount())
		}
		cancel()
		<-done
	})
}
