package workspace

import (
	"reflect"
	"testing"
)

func TestNormalizeFolderPath(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"", "Drafts"},
		{"  >  › ", "Drafts"},
		{" Kari Nordmann › GitHub >  > Vuln search ", "Kari Nordmann > GitHub > Vuln search"},
	} {
		if got, err := normalizeFolderPath(test.input); err != nil || got != test.want {
			t.Errorf("normalizeFolderPath(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	for _, input := range []string{"a>b>c>d>e>f>g>h>i>j>k", string(make([]byte, 101))} {
		if _, err := normalizeFolderPath(input); err == nil {
			t.Errorf("normalizeFolderPath accepted %q", input)
		}
	}
}

func TestBuildCatalogueNestsFoldersAndSearchKeepsMatchingBranches(t *testing.T) {
	queries := []SavedQuery{
		{ID: "1", Name: "Vulnerable repos", FolderPath: "Kari > GitHub > Vuln search", Question: "Which repos have critical alerts?"},
		{ID: "2", Name: "admins", FolderPath: "Kari > GitHub"},
		{ID: "3", Name: "Archived", FolderPath: "Kari > GitHub"},
		{ID: "4", Name: "Languages", FolderPath: ""},
	}
	view := buildCatalogue(queries, "")
	if want := []string{"Drafts", "Kari", "Kari > GitHub", "Kari > GitHub > Vuln search"}; !reflect.DeepEqual(view.Paths, want) {
		t.Fatalf("paths = %q, want %q", view.Paths, want)
	}
	kari := view.Folders[1]
	github := kari.Folders[0]
	if kari.Count != 3 || github.Count != 3 || github.Queries[0].Name != "admins" || github.Queries[1].Name != "Archived" ||
		github.Folders[0].Depth != 2 || github.Folders[0].Queries[0].Depth != 2 {
		t.Fatalf("tree = %#v", github)
	}

	view = buildCatalogue(queries, "CRITICAL")
	kari, drafts := view.Folders[1], view.Folders[0]
	github = kari.Folders[0]
	if view.Matches != 1 || !drafts.Hidden || kari.Hidden || github.Hidden || github.Folders[0].Hidden || !github.Queries[0].Hidden || github.Folders[0].Queries[0].Hidden {
		t.Fatalf("search visibility wrong: drafts=%v kari=%v github=%v", drafts.Hidden, kari.Hidden, github.Hidden)
	}
}
