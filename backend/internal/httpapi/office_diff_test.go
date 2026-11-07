package httpapi

import "testing"

func TestOfficeLineDiff(t *testing.T) {
	lines, truncated := officeLineDiff("alpha\nbravo\ncharlie", "alpha\nbravo two\ncharlie\ndelta")
	if truncated {
		t.Fatal("small diff should not be truncated")
	}
	types := make([]string, 0, len(lines))
	for _, line := range lines {
		types = append(types, line["type"].(string))
	}
	want := []string{"equal", "removed", "added", "equal", "added"}
	if len(types) != len(want) {
		t.Fatalf("got %v want %v", types, want)
	}
	for index := range want {
		if types[index] != want[index] {
			t.Fatalf("line %d: got %q want %q (%v)", index, types[index], want[index], types)
		}
	}
}

func TestOfficeLineDiffEqual(t *testing.T) {
	lines, truncated := officeLineDiff("same\ntext", "same\ntext")
	if truncated {
		t.Fatal("equal diff should not be truncated")
	}
	if len(lines) != 2 {
		t.Fatalf("expected two equal lines, got %d", len(lines))
	}
	for _, line := range lines {
		if line["type"] != "equal" {
			t.Fatalf("expected equal line, got %v", line)
		}
	}
}
