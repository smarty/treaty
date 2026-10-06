package golang

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/smarty/treaty/internal/rules"
)

func TestProofFindsExamplesAndErrors(t *testing.T) {
	root := t.TempDir()
	for name, text := range map[string]string{
		"go.mod": "module example.com/m\n",
		"store/store.go": `package store

import (
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("not found")

var ErrClosed = errors.New("closed")

type ConflictError struct{ Key string }

func (this *ConflictError) Error() string { return this.Key }

// Get finds a value.
//
// Errors:
//   - ErrNotFound: no value has the key.
//   - ErrMissing: never returned.
//   - io.EOF: the stream ended.
func Get(key string) (string, error) {
	if key == "" {
		return "", ErrNotFound
	}

	return lookup(key)
}

func lookup(key string) (string, error) {
	switch key {
	case "x":
		return "", conflict(key)
	}

	return key, nil
}

func conflict(key string) error {
	return &ConflictError{Key: key}
}

// Put stores a value.
//
// Errors:
//   - ErrClosed: the store is closed.
func Put(key string) error {
	err := check()
	if errors.Is(err, ErrNotFound) || err == ErrNotFound {
		return nil
	}

	var conflict *ConflictError
	if errors.As(err, &conflict) {
		return nil
	}

	return fmt.Errorf("put: %w", ErrClosed)
}

func check() error { return nil }

// Size counts values.
func Size() int { return 0 }

// Peek looks without failing.
func Peek(key string) string {
	if value, err := lookup(key); err == nil {
		return value
	}

	_, _ = lookup(key)
	lookup(key)
	if _, err := lookup(key); err != nil {
		return "missing"
	}

	return ""
}

// Fetch passes a helper's error on.
func Fetch(key string) (string, error) {
	value, err := lookup(key)
	if err != nil {
		return struct{ s string }{}.s, fmt.Errorf("fetch: %w", err)
	}

	return value, nil
}
`,
		"store/store_test.go": `package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestGet(t *testing.T) {
	if _, err := Get(""); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func ExampleGet() {
	value, _ := Get("a")
	fmt.Println(value)
	// Output: a
}

func ExamplePut() {
	_ = Put("a")
}

func FuzzGet(f *testing.F) {
	f.Fuzz(func(t *testing.T, key string) { Get(key) })
}

func TestConflictError_Error(t *testing.T) {
	_ = (&ConflictError{}).Error()
}
`,
		"app/app.go": `package app

import "example.com/m/store"

// Load loads.
func Load() error {
	_, err := store.Get("k")
	return err
}

// Save saves.
func Save() error {
	return store.Put("k")
}
`,
	} {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	g, err := NewExtractor().Extract(root)
	if err != nil {
		t.Fatal(err)
	}

	cases, err := NewTestSuite().Discover(root, g)
	if err != nil {
		t.Fatal(err)
	}

	kinds := map[string]string{}
	var tests []rules.TestUse
	for _, each := range cases {
		kinds[each.Name] = each.Kind
		tests = append(tests, rules.TestUse{ID: each.Module + "#" + each.Name, Kind: each.Kind, Targets: each.Targets})
	}

	want := map[string]string{"TestGet": "test", "ExampleGet": "example", "FuzzGet": "fuzz", "TestConflictError_Error": "test"}
	if len(kinds) != len(want) {
		t.Errorf("an example without an Output comment is not run, so it is not found: %v", kinds)
	}

	for name, kind := range want {
		if kinds[name] != kind {
			t.Errorf("%s: kind %q, want %q", name, kinds[name], kind)
		}
	}

	metrics := rules.Metrics(g)
	proofs := rules.Proof(g, tests, map[string]bool{"go": true}, metrics)
	same := func(what string, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", what, got, want)
		}
	}

	get := proofs["go:store:Get"]
	same("Get's examples leave out the fuzz test", get.Examples, "go:store#ExampleGet", "go:store#TestGet")
	same("Get declares", get.Declared, "go:store:ErrMissing", "go:store:ErrNotFound")
	same("Get's declared errors from outside the repository", get.External, "io.EOF")
	same("Get returns what it names and what its helper builds", get.Reachable, "go:store:ConflictError", "go:store:ErrNotFound")
	same("Get's tests name", get.Asserted, "go:store:ErrNotFound")

	put := proofs["go:store:Put"]
	same("Put returns only ErrClosed: comparing and errors.As do not return", put.Reachable, "go:store:ErrClosed")
	same("Put has no examples", put.Examples)

	same("a type counts its methods' tests", proofs["go:store:ConflictError"].Examples, "go:store#TestConflictError_Error")
	same("errors a contract checks and drops are not returned", proofs["go:store:Peek"].Reachable)
	same("an error assigned and returned later is", proofs["go:store:Fetch"].Reachable, "go:store:ConflictError")
	same("a call into another module takes the callee's Errors section", proofs["go:app:Load"].Reachable, "go:store:ErrNotFound")
	same("and its declared errors only", proofs["go:app:Save"].Reachable, "go:store:ErrClosed")
	if !get.Documented || proofs["go:store:Size"].Documented {
		t.Error("only a contract with an Errors section is documented")
	}

	store := metrics["go:store"]
	got := [8]any{store.Measured, store.Contracts, store.ExampleContracts, store.Examples, store.Errors, store.ErrorsProven, store.Undeclared, store.Unreturned}
	if want := [8]any{true, 8, 3, 3, 5, 1, 2, 1}; got != want {
		t.Errorf("store's counts: measured, contracts, with examples, examples, errors, proven, undeclared, unreturned = %v, want %v", got, want)
	}

	if app := metrics["go:app"]; app.Errors != 2 || app.Undeclared != 2 || app.ErrorsProven != 0 {
		t.Errorf("app's errors: %+v", app)
	}

	if unmeasured := rules.Proof(g, tests, map[string]bool{}, rules.Metrics(g)); len(unmeasured) != 0 {
		t.Errorf("a language without tests Treaty can find is not measured: %v", unmeasured)
	}
}
