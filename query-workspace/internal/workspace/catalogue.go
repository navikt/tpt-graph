package workspace

import (
	"sort"
	"strings"
)

// folderView is one folder in the saved-query tree. Folders exist only as prefixes of query paths.
type folderView struct {
	Name, Path string
	Depth      int
	Count      int
	Queries    []catalogueItem
	Folders    []*folderView
	Hidden     bool
}

type catalogueItem struct {
	SavedQuery
	Depth  int
	Hidden bool
}

type catalogueView struct {
	Folders []*folderView
	// Paths lists every folder path, including partial paths, for the save form's suggestions.
	Paths   []string
	Matches int
}

// buildCatalogue groups queries by folder path. Within a folder, its own queries come before its
// subfolders; both are sorted alphabetically. A search hides queries whose name, path and question
// don't match, and folders left without a visible query.
func buildCatalogue(queries []SavedQuery, search string) catalogueView {
	root := &folderView{Depth: -1}
	index := map[string]*folderView{"": root}
	search = strings.ToLower(strings.TrimSpace(search))
	var view catalogueView
	for _, query := range queries {
		if query.FolderPath == "" {
			query.FolderPath = defaultFolder
		}
		parent := root
		for segment := range strings.SplitSeq(query.FolderPath, folderSeparator) {
			path := segment
			if parent.Path != "" {
				path = parent.Path + folderSeparator + segment
			}
			folder, ok := index[path]
			if !ok {
				folder = &folderView{Name: segment, Path: path, Depth: parent.Depth + 1, Hidden: true}
				index[path] = folder
				parent.Folders = append(parent.Folders, folder)
				view.Paths = append(view.Paths, path)
			}
			parent = folder
		}
		matched := strings.Contains(strings.ToLower(query.Name+" "+query.FolderPath+" "+query.Question), search)
		if matched {
			view.Matches++
		}
		parent.Queries = append(parent.Queries, catalogueItem{SavedQuery: query, Depth: parent.Depth, Hidden: !matched})
	}
	finishFolder(root)
	view.Folders = root.Folders
	sort.Strings(view.Paths)
	return view
}

// finishFolder sorts the folder, counts the queries inside it and decides its visibility.
func finishFolder(folder *folderView) {
	sort.SliceStable(folder.Queries, func(i, j int) bool {
		return strings.ToLower(folder.Queries[i].Name) < strings.ToLower(folder.Queries[j].Name)
	})
	sort.SliceStable(folder.Folders, func(i, j int) bool {
		return strings.ToLower(folder.Folders[i].Name) < strings.ToLower(folder.Folders[j].Name)
	})
	folder.Count = len(folder.Queries)
	for _, item := range folder.Queries {
		if !item.Hidden {
			folder.Hidden = false
		}
	}
	for _, child := range folder.Folders {
		finishFolder(child)
		folder.Count += child.Count
		if !child.Hidden {
			folder.Hidden = false
		}
	}
}
