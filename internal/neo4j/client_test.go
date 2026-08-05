package neo4j

import (
	"strings"
	"testing"
	"time"

	neodriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func TestModuleSync_FormattedTime(t *testing.T) {
	tests := []struct {
		name string
		sync ModuleSync
		want string
	}{
		{
			name: "zero value returns unknown",
			sync: ModuleSync{},
			want: "unknown",
		},
		{
			name: "non-zero formats as UTC string",
			sync: ModuleSync{LastSync: time.Date(2024, 3, 15, 9, 30, 0, 0, time.UTC)},
			want: "2024-03-15 09:30 UTC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sync.FormattedTime(); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestModuleSync_StatusColor(t *testing.T) {
	tests := []struct {
		name string
		sync ModuleSync
		want string
	}{
		{
			name: "zero value is red",
			sync: ModuleSync{},
			want: "red",
		},
		{
			name: "less than 24h is green",
			sync: ModuleSync{LastSync: time.Now().Add(-1 * time.Hour)},
			want: "green",
		},
		{
			name: "between 24h and 72h is yellow",
			sync: ModuleSync{LastSync: time.Now().Add(-48 * time.Hour)},
			want: "yellow",
		},
		{
			name: "more than 72h is red",
			sync: ModuleSync{LastSync: time.Now().Add(-100 * time.Hour)},
			want: "red",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.sync.StatusColor(); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestDependencyUsage_ShortImage(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  string
	}{
		{
			name:  "strips registry and repo prefix",
			image: "europe-north1-docker.pkg.dev/nais-management-233d/aap/aap-api:abc1234",
			want:  "aap-api:abc1234",
		},
		{
			name:  "no slash returns image unchanged",
			image: "myimage:latest",
			want:  "myimage:latest",
		},
		{
			name:  "empty string returns empty string",
			image: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DependencyUsage{RunningImage: tt.image}
			if got := d.ShortImage(); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestDependencyUsage_ShortCommit(t *testing.T) {
	tests := []struct {
		name   string
		commit string
		want   string
	}{
		{
			name:   "long SHA is truncated to 7 characters",
			commit: "abc1234def5678",
			want:   "abc1234",
		},
		{
			name:   "exactly 7 characters is returned unchanged",
			commit: "abc1234",
			want:   "abc1234",
		},
		{
			name:   "short SHA is returned unchanged",
			commit: "abc",
			want:   "abc",
		},
		{
			name:   "empty string returns empty string",
			commit: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DependencyUsage{DeployedCommit: tt.commit}
			if got := d.ShortCommit(); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

func TestVersionToRegex(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", ""},
		{"1.2.3", `^1\.2\.3$`},
		{"4.*", `^4\..*$`},
		{"*", `^.*$`},
		{"1.2.*", `^1\.2\..*$`},
		{"*.*.3", `^.*\..*\.3$`},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := versionToRegex(tt.input)
			if got != tt.want {
				t.Errorf("versionToRegex(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindDependencyUsagesQuery_FiltersActiveDeployments(t *testing.T) {
	if !strings.Contains(findDependencyUsagesQuery, "is_active") {
		t.Error("findDependencyUsagesQuery must filter NaisDeployment nodes by is_active = true to exclude inactive deployments")
	}
}

func TestFindNamespaceByIngressQuery_ResolvesWorkloads(t *testing.T) {
	for _, want := range []string{"TARGETS", "WORKLOAD_PARENT", "RUNS_IMAGE", "NaisApp"} {
		if !strings.Contains(findNamespaceByIngressQuery, want) {
			t.Errorf("findNamespaceByIngressQuery must traverse %q to resolve the owning Nais workload", want)
		}
	}
}

func TestFindNamespaceByPathQuery_ResolvesWorkloads(t *testing.T) {
	for _, want := range []string{"TARGETS", "WORKLOAD_PARENT", "RUNS_IMAGE", "NaisApp"} {
		if !strings.Contains(findNamespaceByPathQuery, want) {
			t.Errorf("findNamespaceByPathQuery must traverse %q to resolve the owning Nais workload", want)
		}
	}
}

func TestStringSliceField(t *testing.T) {
	tests := []struct {
		name string
		rec  *neodriver.Record
		want []string
	}{
		{
			name: "collect() result with values",
			rec:  &neodriver.Record{Keys: []string{"workloads"}, Values: []any{[]any{"tpt-graph", "tpt-backend"}}},
			want: []string{"tpt-graph", "tpt-backend"},
		},
		{
			name: "collect() result with null entries from unmatched OPTIONAL MATCH is filtered out",
			rec:  &neodriver.Record{Keys: []string{"workloads"}, Values: []any{[]any{nil, "tpt-graph", ""}}},
			want: []string{"tpt-graph"},
		},
		{
			name: "missing key returns nil",
			rec:  &neodriver.Record{Keys: []string{}, Values: []any{}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringSliceField(tt.rec, "workloads")
			if len(got) != len(tt.want) {
				t.Fatalf("want %v, got %v", tt.want, got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("index %d: want %q, got %q", i, tt.want[i], got[i])
				}
			}
		})
	}
}
