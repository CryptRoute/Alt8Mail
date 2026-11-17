package httpapi

import "testing"

func folderFixtures() []map[string]any {
	return []map[string]any{
		{"id": "inbox", "label": "Inbox", "system": true, "section": "favorites"},
		{"id": "starred", "label": "Starred", "virtual": true, "section": "favorites"},
		{"id": "a", "label": "A", "parentId": "inbox"},
		{"id": "b", "label": "B", "parentId": "a"},
		{"id": "delegated", "label": "Shared", "delegated": true},
	}
}

func TestFolderParentDepthAndDescendants(t *testing.T) {
	folders := folderFixtures()
	if depth, ok := folderParentDepth(folders, ""); !ok || depth != 0 {
		t.Fatalf("top-level depth = %d, %v", depth, ok)
	}
	if depth, ok := folderParentDepth(folders, "inbox"); !ok || depth != 1 {
		t.Fatalf("inbox depth = %d, %v", depth, ok)
	}
	if depth, ok := folderParentDepth(folders, "b"); !ok || depth != 3 {
		t.Fatalf("nested depth = %d, %v", depth, ok)
	}
	if !folderIsDescendant(folders, "b", "inbox") {
		t.Fatal("b should be a descendant of inbox")
	}
	if folderIsDescendant(folders, "inbox", "b") {
		t.Fatal("inbox must not be a descendant of b")
	}
	if _, ok := folderParentDepth(folders, "missing"); ok {
		t.Fatal("missing parent reported a valid depth")
	}
}

func TestFolderParentCandidate(t *testing.T) {
	folders := folderFixtures()
	if folderParentCandidate(folders, "starred") != nil {
		t.Fatal("virtual folder was accepted as a parent")
	}
	if folderParentCandidate(folders, "delegated") != nil {
		t.Fatal("delegated folder was accepted as a parent")
	}
	if folderParentCandidate(folders, "inbox") == nil {
		t.Fatal("primary folder should accept subfolders")
	}
	if folderParentCandidate(folders, "a") == nil {
		t.Fatal("custom folder should accept subfolders")
	}
}

func TestNormalizeFolderCollapsedDefault(t *testing.T) {
	normalized := normalizeFolderRecords([]any{
		map[string]any{"id": "a", "label": "A", "section": "folders"},
		map[string]any{"id": "b", "label": "B", "section": "folders", "collapsed": true},
	})
	first, _ := normalized[0].(map[string]any)
	second, _ := normalized[1].(map[string]any)
	if stateBool(first, "collapsed") {
		t.Fatal("new folders should default to expanded")
	}
	if !stateBool(second, "collapsed") {
		t.Fatal("persisted collapsed state was lost")
	}
}
