package app

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// sourceLimit is the most lines source prints from a file at once, when
// they are most of the file, unless the caller asks for all of them.
const sourceLimit = 120

// lineRange is a trailing :line or :start-end on a source target.
var lineRange = regexp.MustCompile(`^(.+):(\d+)(?:-(\d+))?$`)

// Source prints the code of a symbol, a file or a range of a file's lines
// from the working tree, numbered as the file numbers them, so an agent can
// read just what a slice points to.
//
// Notes:
//   - A file, or a range, longer than sourceLimit lines that is most of its
//     file prints the file's outline instead: its declarations with their
//     line ranges, so the reader can ask for the symbols it needs. all
//     prints the lines anyway. A symbol prints whole at any length.
//
// Parameters:
//   - target: a symbol, from its documentation to its last line; a file;
//     or a file with :line or :start-end. Each may be an id, a path or a
//     short name that matches exactly one.
//   - all: print a long file or range in full rather than its outline.
//
// Returns:
//   - result: a header naming the file and lines, then each line with its
//     number.
//   - err: the tree could not be read, or the target is unknown, ambiguous,
//     a module, or a range outside the file.
//
// Errors:
//   - ErrUnknownTarget: nothing matches the target, or it names a module.
//   - ErrAmbiguousTarget: several symbols, modules or files match it.
func (this *Service) Source(target string, all bool) (result string, err error) {
	analysis, err := this.analyze("")
	if err != nil {
		return "", err
	}

	return this.sourceOf(analysis, target, all)
}

// sourceOf prints a target's code from an analysis's working tree.
func (this *Service) sourceOf(analysis *analysis, target string, all bool) (result string, err error) {
	start, end := 0, 0
	if match := lineRange.FindStringSubmatch(target); match != nil && analysis.head.Symbol(target) == nil {
		target = match[1]
		start, _ = strconv.Atoi(match[2])
		end = start
		if match[3] != "" {
			end, _ = strconv.Atoi(match[3])
		}
	}

	resolved, err := analysis.resolve(target)
	if err != nil {
		return "", err
	}

	g := analysis.head
	file, label := resolved, resolved
	if symbol := g.Symbol(resolved); symbol != nil {
		file = symbol.File
		if start == 0 {
			start, end = symbol.Line, max(symbol.EndLine, symbol.Line)
			if symbol.DocLine > 0 {
				start = symbol.DocLine
			}
		}
	} else if g.Module(resolved) != nil {
		return "", fmt.Errorf("%w: %s is a module; give a symbol or one of its files, which slice lists", ErrUnknownTarget, resolved)
	}

	data, err := this.extractor.Source(this.root, file)
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if start == 0 {
		start, end = 1, len(lines)
	}

	if start < 1 || end < start || end > len(lines) {
		return "", fmt.Errorf("%w: %s has lines 1-%d, not %d-%d", ErrUnknownTarget, file, len(lines), start, end)
	}

	if count := end - start + 1; !all && g.Symbol(resolved) == nil && count > sourceLimit && 2*count > len(lines) {
		outline, err := buildSlice(analysis, file)
		if err != nil {
			return "", err
		}

		return fmt.Sprintf("%s:%d-%d is %d of the file's %d lines, more than source prints at once (%d); ask for the symbols you need by name, or for a shorter range, or for all of it.\n\n%s",
			file, start, end, count, len(lines), sourceLimit, outline.Text()), nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "%s:%d-%d", file, start, end)
	if label != file {
		fmt.Fprintf(&builder, "  %s", label)
	}

	builder.WriteString("\n")
	for number := start; number <= end; number++ {
		fmt.Fprintf(&builder, "%6d\t%s\n", number, lines[number-1])
	}

	return builder.String(), nil
}
