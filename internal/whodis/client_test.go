package whodis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLookupTeam_OK(t *testing.T) {
	want := Team{
		Slug:         "appsec",
		SlackChannel: "#appsec",
		Members: []Member{
			{Email: "alice@nav.no", Name: "Alice", Role: "OWNER"},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.LookupTeam(context.Background(), "appsec")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil team")
	}
	if got.Slug != want.Slug {
		t.Errorf("Slug: want %q, got %q", want.Slug, got.Slug)
	}
	if got.SlackChannel != want.SlackChannel {
		t.Errorf("SlackChannel: want %q, got %q", want.SlackChannel, got.SlackChannel)
	}
	if len(got.Members) != 1 || got.Members[0].Email != "alice@nav.no" {
		t.Errorf("Members: unexpected value %+v", got.Members)
	}
}

func TestLookupTeam_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	got, err := c.LookupTeam(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("want nil team, got %+v", got)
	}
}

func TestLookupTeam_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.LookupTeam(context.Background(), "appsec")
	if err == nil {
		t.Error("expected error for 500 response, got nil")
	}
}

func TestLookupTeam_RequestPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Team{Slug: "myteam"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, _ = c.LookupTeam(context.Background(), "myteam")

	if !strings.HasSuffix(gotPath, "/nais/myteam") {
		t.Errorf("unexpected request path %q, want suffix /nais/myteam", gotPath)
	}
}

func TestLookupTeam_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	_, err := c.LookupTeam(context.Background(), "appsec")
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}
