# CML: syntax proposal

Sep 28, 2026 · companion to *Treaty: design document*

## Summary

The design document now specifies CML in full; see its "Diagram language (CML)" section. This companion records how the language got there: what was decided, why, and which alternatives were rejected. Read it for the reasoning, not the spec.

In one line: **CML is paths as headers, each language's own declarations beneath them, nesting for ownership and `@` directives for everything that isn't code.** For code it exists only in memory, and design files are its only text on disk.

## Decisions, in order

| # | Decision | Outcome |
| --- | --- | --- |
| 1 | What is CML for? | Structure only. Scores are computed on demand and kept in memory. |
| 2 | Is anything tracked? | No. The tool is ephemeral: for review and design thinking, not for communicating design between people. |
| 3 | Where do files go? | Everything under `.treaty/`, which ignores itself with a `.gitignore` containing `*`. Designs go in `.treaty/designs/`. |
| 4 | Does code get a `.cml` file? | No. Generated CML exists only in memory. `treaty dump` prints it to stdout for debugging and golden tests. |
| 5 | CI upload and PR comment? | Removed. |
| 6 | SARIF output? | Removed. `treaty check` writes report JSON on stdout and sets the exit code. |
| 7 | How does a design start? | `treaty design new <name> [--from <target>...]` scaffolds it from today's contracts. The map displays designs but never edits them. |
| 8 | Syntax | Paths as headers and native declarations beneath, described below. |

## Consequences of being ephemeral

Several requirements in the first draft of the design document assumed CML would be a persistent file. They fell away:

- **Git diffs.** "Must diff cleanly in git" became "output must be deterministic", which golden tests need.
- **Content-hash references and stale markers.** Every graph is built from the tree it describes, so nothing can go stale. Symbol ids are enough. A hash of each function body survives only as the key for the mutation-result cache.
- **A versioned JSON graph schema.** No graph file exists to version. JSON remains only for output that leaves the process on stdout: the check report and slices.
- **`treaty export`.** Removed. `treaty map` builds, scores and renders in one pass.
- **Round-tripping.** It became an internal test: graph → `treaty dump` → parser → the same graph.
- **Mermaid compatibility.** No longer a question.
- **Where design files live.** Settled as `.treaty/designs/`.

## The syntax

```
cml 1
design "Scoped lifetimes"

go:injection
  type Container interface
    func Resolve(ctx context.Context, k Key) (any, error)
    func Scope() scope.Scope

go:injection/scope/scope.go
  @forbid go:injection/ports
  type Scope interface
    func Dispose() error
      @expect strength >= 0.80
  func New(parent injection.Container) Scope
    @depends go:injection:Container
```

Why each piece is the way it is:

| Choice | Reason |
| --- | --- |
| Native declarations, one per line | People sketching a Go design think in Go. A neutral notation loses information, and breaking-change rules are per language anyway. |
| Nesting instead of `Type.Method` names | Indentation shows ownership, the way it reads in the source. |
| No receivers | Receivers sit below the level a contract map works at. The graph still records pointer vs value, the contract diff still reports a change as breaking, and the inspector shows it. |
| Headers are `<lang>:<path>` with the file optional | A path ending in a source extension names a file; any other path names a module, for designs where no file has been chosen. Symbols are matched by id, so moving code between files never counts as drift. |
| Layers come from the config only | `treaty.yaml` already places every module, including ones a design invents. A module that matches no glob shows up as Unclassified, a useful finding in itself. |
| `@` marks directives | Bare keywords can collide with code: in C, `expect strength` parses as a declaration when `expect` is a typedef. No supported language starts a declaration with `@`. |
| A fixed set of directives | Every check is already a short, closed list. An expression language would add an evaluator to the core. |
| CML's parser reads only headers, indentation and directives | The text of each declaration goes to that language's hand-written contract scanner, the same one that reads source, which rebuilds a real declaration from the nesting. Backticks in Go struct tags need no escaping. |
| Contract-ness comes from native syntax | In Go, exported names; for a method, an exported method on an exported type. No `internal` marker is needed. |

## Alternatives rejected

**Extending Mermaid.** Mermaid has no extension point, and its node-shape syntax (`A(x)`, `A[x]`, `A{x}`) clashes with the brackets in every signature.

```
contractmap
  subgraph graph["internal/graph"]:::domain
    Graph.Order("func (g *Graph) Order() ([]Binding, error)")
  end
```

**A YAML or TOML schema.** It takes several times as many lines to write, and signatures containing `: ` need quotes. It also adds a library to a core with no other dependencies.

```yaml
- id: go:injection/scope
  symbols:
    - {name: Scope.Dispose, kind: method, sig: "func (Scope) Dispose() error"}
```

**Keyword lines with signatures in backtick spans.** This was the first proposal. It worked, but it repeated each language's kind in CML's own words and needed receiver spellings that aren't valid Go. It also listed an interface's methods twice: once in the interface's signature and again as method lines.

```
module go:injection/scope application
  interface Scope `type Scope interface{ Dispose() error }`
  method Scope.Dispose `func (Scope) Dispose() error`
    expect strength >= 0.80
```

## Known rough edges

The plan is to find and fix these as the language gets used, not to design them away up front.

- **Stripping dump tokens.** `treaty dump` strips trailing `@<line>` and `@ptr` tokens off each line. A declaration ending in text that looks like one of these would be misread. Go struct tags end in a backtick, so they are safe.
- **Go generics.** Type parameters on methods are declared on the receiver's type. With receivers hidden, the snippet parser has to take them from the enclosing type line.
- **Go only for now.** Version 1 reads Go alone. Each later language needs its own contract scanner behind the same extractor interface.
- **Modules that span packages.** Whether a module can span several packages is still open. Headers assume one path per module.
- **CML's final name.** Still open.
