package cml

import (
	"errors"
	"testing"
)

const design = `cml 1
design "Scoped lifetimes"

// A comment.
go:injection/scope/scope.go
  @forbid go:injection/ports
  type Scope interface
    func Dispose() error
      @depends go:injection/errs:ErrDisposed
  func New(parent injection.Container) Scope
    @depends go:injection:Container
`

func TestParseDesign(t *testing.T) {
	document, err := Parse(design)
	if err != nil {
		t.Fatal(err)
	}

	if document.Design != "Scoped lifetimes" || len(document.Blocks) != 1 {
		t.Fatalf("document: %+v", document)
	}

	block := document.Blocks[0]
	if block.Language != "go" || block.Path != "injection/scope/scope.go" || len(block.Directives) != 1 || len(block.Nodes) != 2 {
		t.Fatalf("block: %+v", block)
	}

	scope := block.Nodes[0]
	if scope.Text != "type Scope interface" || len(scope.Children) != 1 || scope.Children[0].Directives[0].Name != "depends" {
		t.Fatalf("scope: %+v", scope)
	}

	if document.IsDump() {
		t.Fatal("a design is not a dump")
	}

	if printed := Print(document); printed != `cml 1
design "Scoped lifetimes"

go:injection/scope/scope.go
  @forbid go:injection/ports
  type Scope interface
    func Dispose() error
      @depends go:injection/errs:ErrDisposed
  func New(parent injection.Container) Scope
    @depends go:injection:Container
` {
		t.Fatalf("printed:\n%s", printed)
	}
}

func TestParseDumpTokens(t *testing.T) {
	document, err := Parse("cml 1\n\ngo:a/b.go\n  type T struct @3\n    Tag string `json:\"tag\"`\n    func M() @other.go:9 @ptr\n      @call go:a:f @10\n")
	if err != nil {
		t.Fatal(err)
	}

	typ := document.Blocks[0].Nodes[0]
	field, method := typ.Children[0], typ.Children[1]
	if typ.SourceLine != 3 || field.Text != "Tag string `json:\"tag\"`" || field.SourceLine != 0 {
		t.Fatalf("type or field: %+v %+v", typ, field)
	}

	if method.Text != "func M()" || method.File != "other.go" || method.SourceLine != 9 || !method.Pointer || method.Directives[0].Args[0] != "go:a:f" {
		t.Fatalf("method: %+v", method)
	}

	if !document.IsDump() {
		t.Fatal("locations make it a dump")
	}
}

func TestParseErrors(t *testing.T) {
	for name, text := range map[string]string{
		"no version":        "go:a\n",
		"tab indent":        "cml 1\ngo:a\n\tfunc F()\n",
		"odd indent":        "cml 1\ngo:a\n   func F()\n",
		"too deep":          "cml 1\ngo:a\n      func F()\n",
		"before header":     "cml 1\n  func F()\n",
		"unknown directive": "cml 1\ngo:a\n  @maybe x\n",
		"bad header":        "cml 1\nnot a header\n",
	} {
		t.Run(name, func(t *testing.T) {
			var parseErr *Error
			if _, err := Parse(text); !errors.As(err, &parseErr) {
				t.Fatalf("want *Error, got %v", err)
			}
		})
	}
}
