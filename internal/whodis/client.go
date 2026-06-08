package whodis

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Team represents the ownership information returned by whodis.
type Team struct {
	Slug         string   `json:"slug"`
	SlackChannel string   `json:"slackChannel"`
	Purpose      string   `json:"purpose"`
	Members      []Member `json:"members"`
}

// Member is a single team member with their role.
type Member struct {
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

// Client fetches team ownership data from the whodis service.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient returns a Client that targets the given base URL (e.g. "http://whodis").
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// LookupTeam fetches ownership information for the given team slug.
// Returns (nil, nil) when the team is not found (HTTP 404).
func (c *Client) LookupTeam(ctx context.Context, teamSlug string) (*Team, error) {
	url := fmt.Sprintf("%s/nais/%s", c.baseURL, teamSlug)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whodis request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("whodis returned unexpected status %d", resp.StatusCode)
	}

	var team Team
	if err := json.NewDecoder(resp.Body).Decode(&team); err != nil {
		return nil, fmt.Errorf("decode whodis response: %w", err)
	}

	return &team, nil
}
