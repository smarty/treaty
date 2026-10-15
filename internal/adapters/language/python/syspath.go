package python

import (
	"path"
	"strconv"
	"strings"
)

// sysPathArguments finds each directory a file adds to the import path at
// run time: the path argument of sys.path.insert(i, p), sys.path.append(p)
// and site.addsitedir(p).
//
// Parameters:
//   - tokens: the file's tokens.
//
// Returns:
//   - result: each call's path argument, as tokens.
func sysPathArguments(tokens []token) (result [][]token) {
	for index := 0; index+4 < len(tokens); index++ {
		var call string
		var open int
		switch {
		case tokens[index].is("sys") && tokens[index+1].is(".") && tokens[index+2].is("path") && tokens[index+3].is(".") && index+5 < len(tokens) && tokens[index+5].is("("):
			call, open = tokens[index+4].text, index+5
		case tokens[index].is("site") && tokens[index+1].is(".") && tokens[index+2].is("addsitedir") && tokens[index+3].is("("):
			call, open = "addsitedir", index+3
		default:
			continue
		}

		if index > 0 && tokens[index-1].is(".") {
			continue
		}

		end := matching(tokens, open)
		if end <= open {
			continue
		}

		arguments := splitTop(tokens[open+1:end], ",")
		switch {
		case call == "insert" && len(arguments) == 2:
			result = append(result, arguments[1])
		case (call == "append" || call == "addsitedir") && len(arguments) == 1:
			result = append(result, arguments[0])
		}
	}

	return result
}

// searchDirs works out the directories a path argument can name, from the
// repository root, for the file at file. A path built from __file__ names
// one directory. A plain relative string depends on the working directory,
// so it may be the file's directory or the repository root: both are
// candidates. A path computed any other way names none.
//
// Parameters:
//   - file: the adding file, from the repository root.
//   - argument: the path argument's tokens.
//
// Returns:
//   - result: candidate directories, cleaned, from the repository root.
func searchDirs(file string, argument []token) (result []string) {
	value, consumed, ok := (&pathExpression{file: file, tokens: argument}).sum(0)
	if !ok || consumed != len(argument) {
		return nil
	}

	if value.anchored {
		dir := strings.TrimPrefix(path.Clean(value.path), "/")
		if dir == "" {
			dir = "."
		}

		return []string{dir}
	}

	if path.IsAbs(value.path) {
		return nil
	}

	return []string{path.Join(path.Dir(file), value.path), path.Clean(value.path)}
}

// pathValue is a path an expression computes: anchored at the repository
// root, written with a leading /, when built from __file__; else relative
// to the working directory.
type pathValue struct {
	path     string
	anchored bool
}

// join appends a relative part to the value.
func (this pathValue) join(part string) (pathValue, bool) {
	if path.IsAbs(part) {
		return pathValue{}, false
	}

	return pathValue{path: path.Join(this.path, part), anchored: this.anchored}, true
}

// pathExpression evaluates the few path expressions found in sys.path
// additions: string literals, __file__, os.path's dirname, join, abspath,
// realpath and normpath, str, os.fspath, and pathlib's Path with parent,
// parents[n], resolve, absolute, joinpath and /.
type pathExpression struct {
	file   string
	tokens []token
}

func (this *pathExpression) at(index int) token {
	if index < len(this.tokens) {
		return this.tokens[index]
	}

	return token{}
}

// sum reads a primary and any parts joined to it with /.
func (this *pathExpression) sum(index int) (value pathValue, next int, ok bool) {
	if value, next, ok = this.postfix(index); !ok {
		return pathValue{}, index, false
	}

	for this.at(next).is("/") {
		part, after, ok := this.postfix(next + 1)
		if !ok || part.anchored {
			return pathValue{}, index, false
		}

		if value, ok = value.join(part.path); !ok {
			return pathValue{}, index, false
		}

		next = after
	}

	return value, next, true
}

// postfix reads a primary and the attribute and call chain after it.
func (this *pathExpression) postfix(index int) (value pathValue, next int, ok bool) {
	if value, next, ok = this.primary(index); !ok {
		return pathValue{}, index, false
	}

	for this.at(next).is(".") {
		name := this.at(next + 1).text
		switch {
		case name == "parent":
			value.path, next = path.Dir(value.path), next+2
		case name == "parents" && this.at(next+2).is("[") && this.at(next+4).is("]"):
			count, err := strconv.Atoi(this.at(next + 3).text)
			if err != nil || count < 0 {
				return pathValue{}, index, false
			}

			for range count + 1 {
				value.path = path.Dir(value.path)
			}

			next += 5
		case (name == "resolve" || name == "absolute") && this.at(next+2).is("(") && this.at(next+3).is(")"):
			next += 4
		case name == "joinpath" && this.at(next+2).is("("):
			parts, after, ok := this.arguments(next + 2)
			if !ok {
				return pathValue{}, index, false
			}

			for _, part := range parts {
				if part.anchored {
					return pathValue{}, index, false
				}

				if value, ok = value.join(part.path); !ok {
					return pathValue{}, index, false
				}
			}

			next = after
		default:
			return value, next, true
		}
	}

	return value, next, true
}

// primary reads a string, __file__, a call of a known function, or an
// expression in parentheses.
func (this *pathExpression) primary(index int) (value pathValue, next int, ok bool) {
	current := this.at(index)
	if text, ok := unquote(current); ok {
		return pathValue{path: text}, index + 1, true
	}

	if current.is("__file__") {
		return pathValue{path: "/" + this.file, anchored: true}, index + 1, true
	}

	if current.is("(") {
		value, next, ok := this.sum(index + 1)
		if !ok || !this.at(next).is(")") {
			return pathValue{}, index, false
		}

		return value, next + 1, true
	}

	// A dotted function name, such as os.path.join or pathlib.Path.
	var name []string
	next = index
	for this.at(next).kind == tokenIdent {
		name = append(name, this.at(next).text)
		if !this.at(next+1).is(".") || this.at(next+2).kind != tokenIdent {
			next++
			break
		}

		next += 2
	}

	if len(name) == 0 || !this.at(next).is("(") {
		return pathValue{}, index, false
	}

	arguments, after, ok := this.arguments(next)
	if !ok {
		return pathValue{}, index, false
	}

	function := strings.Join(name, ".")
	switch {
	case function == "os.path.join" && len(arguments) > 0:
		value = arguments[0]
		for _, part := range arguments[1:] {
			if part.anchored {
				return pathValue{}, index, false
			}

			if value, ok = value.join(part.path); !ok {
				return pathValue{}, index, false
			}
		}

		return value, after, true
	case len(arguments) != 1:
		return pathValue{}, index, false
	case function == "os.path.dirname":
		value = arguments[0]
		value.path = path.Dir(value.path)
		return value, after, true
	case function == "os.path.abspath" || function == "os.path.realpath" || function == "os.path.normpath" || function == "str" ||
		function == "os.fspath" || function == "Path" || function == "pathlib.Path":
		return arguments[0], after, true
	}

	return pathValue{}, index, false
}

// arguments reads the comma-separated arguments of a call whose ( is at
// open.
func (this *pathExpression) arguments(open int) (result []pathValue, next int, ok bool) {
	end := matching(this.tokens, open)
	if end <= open {
		return nil, open, false
	}

	for _, part := range splitTop(this.tokens[open+1:end], ",") {
		value, consumed, ok := (&pathExpression{file: this.file, tokens: part}).sum(0)
		if !ok || consumed != len(part) {
			return nil, open, false
		}

		result = append(result, value)
	}

	return result, end + 1, true
}
