package app

import "strings"

const (
	DiffAdded     = "+"
	DiffRemoved   = "-"
	DiffUnchanged = " "
)

// maxDiffLines is the most differing lines either side of a diff may have
// before the diff stops looking for common lines and shows the old code
// removed and the new code added.
const maxDiffLines = 2000

// DiffLine is one line of a symbol's code diff: removed, added or kept.
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// lineDiff compares two texts line by line, keeping the longest run of
// common lines and marking the rest removed or added, removals first.
//
// Notes:
//   - Lines the texts start and end with in common are kept before the
//     search, so a whole file with a few edits is cheap to compare.
//
// Parameters:
//   - before: the old text.
//   - after: the new text.
//
// Returns:
//   - result: the lines of both, in order, each marked.
func lineDiff(before, after string) (result []DiffLine) {
	a, b := splitLines(before), splitLines(after)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}

	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}

	for _, text := range a[:prefix] {
		result = append(result, DiffLine{Op: DiffUnchanged, Text: text})
	}

	result = append(result, middleDiff(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix])...)
	for _, text := range a[len(a)-suffix:] {
		result = append(result, DiffLine{Op: DiffUnchanged, Text: text})
	}

	return result
}

// middleDiff marks the lines between the common start and end of two texts.
// Past maxDiffLines on either side it stops looking for common lines and
// shows the old lines removed and the new ones added.
func middleDiff(a, b []string) (result []DiffLine) {
	if len(a) > maxDiffLines || len(b) > maxDiffLines {
		for _, text := range a {
			result = append(result, DiffLine{Op: DiffRemoved, Text: text})
		}

		for _, text := range b {
			result = append(result, DiffLine{Op: DiffAdded, Text: text})
		}

		return result
	}

	// common[i][j] is the length of the longest common subsequence of a[i:]
	// and b[j:].
	common := make([][]int, len(a)+1)
	for i := range common {
		common[i] = make([]int, len(b)+1)
	}

	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				common[i][j] = common[i+1][j+1] + 1
			} else {
				common[i][j] = max(common[i+1][j], common[i][j+1])
			}
		}
	}

	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			result = append(result, DiffLine{Op: DiffUnchanged, Text: a[i]})
			i++
			j++
		case j == len(b) || i < len(a) && common[i+1][j] >= common[i][j+1]:
			result = append(result, DiffLine{Op: DiffRemoved, Text: a[i]})
			i++
		default:
			result = append(result, DiffLine{Op: DiffAdded, Text: b[j]})
			j++
		}
	}

	return result
}

// linesOf cuts lines start to end, counting from 1, out of a file's text,
// or empty when the file or the lines are missing.
func linesOf(text string, start, end int) string {
	if text == "" || start < 1 {
		return ""
	}

	lines := strings.Split(text, "\n")
	end = min(max(end, start), len(lines))
	if start > end {
		return ""
	}

	return strings.Join(lines[start-1:end], "\n")
}

func splitLines(text string) []string {
	if text == "" {
		return nil
	}

	return strings.Split(strings.TrimRight(text, "\n"), "\n")
}
