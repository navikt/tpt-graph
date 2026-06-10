package neo4j

import (
	"testing"
	"time"
)

// --- ModuleSync.FormattedTime ---

func TestModuleSync_FormattedTime_Zero(t *testing.T) {
	m := ModuleSync{}
	if got := m.FormattedTime(); got != "unknown" {
		t.Errorf("want %q, got %q", "unknown", got)
	}
}

func TestModuleSync_FormattedTime_NonZero(t *testing.T) {
	m := ModuleSync{LastSync: time.Date(2024, 3, 15, 9, 30, 0, 0, time.UTC)}
	want := "2024-03-15 09:30 UTC"
	if got := m.FormattedTime(); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

// --- ModuleSync.StatusColor ---

func TestModuleSync_StatusColor_Zero(t *testing.T) {
	m := ModuleSync{}
	if got := m.StatusColor(); got != "red" {
		t.Errorf("want %q, got %q", "red", got)
	}
}

func TestModuleSync_StatusColor_Green(t *testing.T) {
	m := ModuleSync{LastSync: time.Now().Add(-1 * time.Hour)}
	if got := m.StatusColor(); got != "green" {
		t.Errorf("want %q, got %q", "green", got)
	}
}

func TestModuleSync_StatusColor_Yellow(t *testing.T) {
	m := ModuleSync{LastSync: time.Now().Add(-48 * time.Hour)}
	if got := m.StatusColor(); got != "yellow" {
		t.Errorf("want %q, got %q", "yellow", got)
	}
}

func TestModuleSync_StatusColor_Red(t *testing.T) {
	m := ModuleSync{LastSync: time.Now().Add(-100 * time.Hour)}
	if got := m.StatusColor(); got != "red" {
		t.Errorf("want %q, got %q", "red", got)
	}
}

// --- DependencyUsage.ShortImage ---

func TestDependencyUsage_ShortImage_WithPrefix(t *testing.T) {
	d := DependencyUsage{RunningImage: "europe-north1-docker.pkg.dev/nais-management-233d/aap/aap-api:abc1234"}
	want := "aap-api:abc1234"
	if got := d.ShortImage(); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestDependencyUsage_ShortImage_NoSlash(t *testing.T) {
	d := DependencyUsage{RunningImage: "myimage:latest"}
	if got := d.ShortImage(); got != "myimage:latest" {
		t.Errorf("want %q, got %q", "myimage:latest", got)
	}
}

func TestDependencyUsage_ShortImage_Empty(t *testing.T) {
	d := DependencyUsage{}
	if got := d.ShortImage(); got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}

// --- DependencyUsage.ShortCommit ---

func TestDependencyUsage_ShortCommit_Long(t *testing.T) {
	d := DependencyUsage{DeployedCommit: "abc1234def5678"}
	want := "abc1234"
	if got := d.ShortCommit(); got != want {
		t.Errorf("want %q, got %q", want, got)
	}
}

func TestDependencyUsage_ShortCommit_ExactlySeven(t *testing.T) {
	d := DependencyUsage{DeployedCommit: "abc1234"}
	if got := d.ShortCommit(); got != "abc1234" {
		t.Errorf("want %q, got %q", "abc1234", got)
	}
}

func TestDependencyUsage_ShortCommit_Short(t *testing.T) {
	d := DependencyUsage{DeployedCommit: "abc"}
	if got := d.ShortCommit(); got != "abc" {
		t.Errorf("want %q, got %q", "abc", got)
	}
}

func TestDependencyUsage_ShortCommit_Empty(t *testing.T) {
	d := DependencyUsage{}
	if got := d.ShortCommit(); got != "" {
		t.Errorf("want empty string, got %q", got)
	}
}
