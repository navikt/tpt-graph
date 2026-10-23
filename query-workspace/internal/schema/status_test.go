package schema

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"
)

func TestStatusFormatsReadableMetadataAge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		location, err := time.LoadLocation("Europe/Oslo")
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct {
			age  time.Duration
			want string
		}{
			{30 * time.Second, "less than 1m"},
			{87 * time.Minute, "1h 27m"},
			{86*time.Hour + 27*time.Minute, "3d 14h"},
		} {
			store := &testStore{snapshots: []Snapshot{{Target: "target", CapturedAt: time.Now().Add(-test.age)}}}
			cache := NewCache(&testSource{}, store, "target", location, slog.New(slog.NewTextHandler(io.Discard, nil)))
			status, err := cache.Status(context.Background())
			if err != nil || status.Age != test.want {
				t.Fatalf("Status age = %q, error = %v; want %q", status.Age, err, test.want)
			}
		}
	})
}
