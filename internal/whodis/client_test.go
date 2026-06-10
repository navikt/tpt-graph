package whodis

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLookupTeam(t *testing.T) {
	validTeam := Team{
		Slug:         "appsec",
		SlackChannel: "#appsec",
		Members:      []Member{{Email: "alice@nav.no", Name: "Alice", Role: "OWNER"}},
	}

	tests := []struct {
		name     string
		handler  http.HandlerFunc
		slug     string
		wantTeam *Team
		wantErr  bool
		wantPath string
	}{
		{
			name: "200 decodes team correctly",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(validTeam)
			},
			slug:     "appsec",
			wantTeam: &validTeam,
			wantPath: "/nais/appsec",
		},
		{
			name: "404 returns nil team without error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			slug:     "nonexistent",
			wantTeam: nil,
			wantPath: "/nais/nonexistent",
		},
		{
			name: "500 returns error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			slug:    "appsec",
			wantErr: true,
		},
		{
			name: "invalid JSON returns error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("not-json"))
			},
			slug:    "appsec",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				tt.handler(w, r)
			}))
			defer srv.Close()

			c := NewClient(srv.URL)
			got, err := c.LookupTeam(context.Background(), tt.slug)

			if tt.wantErr {
				if err == nil {
					t.Error("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantTeam == nil {
				if got != nil {
					t.Errorf("want nil team, got %+v", got)
				}
				return
			}

			if got == nil {
				t.Fatal("want non-nil team, got nil")
			}
			if got.Slug != tt.wantTeam.Slug {
				t.Errorf("Slug: want %q, got %q", tt.wantTeam.Slug, got.Slug)
			}
			if got.SlackChannel != tt.wantTeam.SlackChannel {
				t.Errorf("SlackChannel: want %q, got %q", tt.wantTeam.SlackChannel, got.SlackChannel)
			}

			if tt.wantPath != "" && !strings.HasSuffix(gotPath, tt.wantPath) {
				t.Errorf("request path: want suffix %q, got %q", tt.wantPath, gotPath)
			}
		})
	}
}
