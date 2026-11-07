package httpapi

import "strings"

const (
	maxDiffLines = 1500
	maxDiffItems = 800
)

// officeLineDiff returns a unified, line-oriented diff. It is intentionally
// bounded: an LCS table above 1500 unchanged lines would create an outsized
// allocation for little practical value, so very large documents are
// represented as a replacement block.
func officeLineDiff(before, after string) ([]map[string]any, bool) {
	a := strings.Split(strings.ReplaceAll(before, "\r\n", "\n"), "\n")
	b := strings.Split(strings.ReplaceAll(after, "\r\n", "\n"), "\n")
	truncated := false
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		a = capStringSlice(a, maxDiffLines)
		b = capStringSlice(b, maxDiffLines)
		truncated = true
	}
	n, m := len(a), len(b)
	width := m + 1
	table := make([]int32, (n+1)*width)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i*width+j] = table[(i+1)*width+j+1] + 1
				continue
			}
			right := table[i*width+j+1]
			down := table[(i+1)*width+j]
			if right >= down {
				table[i*width+j] = right
			} else {
				table[i*width+j] = down
			}
		}
	}
	items := []map[string]any{}
	appendItem := func(kind, text string, fromLine, toLine int) {
		if len(items) >= maxDiffItems {
			truncated = true
			return
		}
		item := map[string]any{"type": kind, "text": text}
		if fromLine > 0 {
			item["fromLine"] = fromLine
		}
		if toLine > 0 {
			item["toLine"] = toLine
		}
		items = append(items, item)
	}
	i, j := 0, 0
	for i < n || j < m {
		if i < n && j < m && a[i] == b[j] {
			appendItem("equal", a[i], i+1, j+1)
			i++
			j++
			continue
		}
		if j < m && (i == n || table[i*width+j+1] > table[(i+1)*width+j]) {
			appendItem("added", b[j], 0, j+1)
			j++
			continue
		}
		if i < n {
			appendItem("removed", a[i], i+1, 0)
			i++
		}
	}
	return items, truncated
}

func capStringSlice(values []string, limit int) []string {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}
