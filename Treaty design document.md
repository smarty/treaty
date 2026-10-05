# Treaty: design document

Sep 28, 2026 · @Timothy Eckstein

> **Renamed Sep 29, 2026.** The tool was called Contract Map, with the binary `cmap`. It is now Treaty: the binary is `treaty`, the config file is `treaty.yaml` and the workspace is `.treaty/`.

## Summary

Treaty is a tool, written in Go, that reads a codebase into one graph, held in memory in the model of a small diagram language, AutoPen (formerly CML). Everything else is drawn from that graph: a hexagonal map for reviewers, mechanical scores, scoped context slices for coding agents, and design files, scaffolded from existing code, where people sketch new modules and contracts before the code exists. Every score must come from static analysis, never from a model's judgment.

The tool is ephemeral. It exists for code review, for thinking through a design and for working alongside a coding agent, not for communicating design between team members. It builds the graph from the tree, scores it and serves what was asked for, keeping everything in memory. A one-shot command discards it on exit, and the live server discards it when it stops. Nothing it writes is ever tracked in git.

It is used in two ways. In code review, it shows what a pull request changes in the architecture. While working with an agent, it runs as a live server: the person watches the map update in a browser as code is edited, and the agent uses the same graph through MCP tools to decide where to work without reading the whole codebase. See Live map and MCP server.

The graph's nodes are contract symbols (exported functions, methods, interfaces, types, values) grouped into modules, which a config file places into the layers, slices or contexts of the architecture the team chose: hexagonal, clean, layered, vertical slices or modular monolith. Its scores answer three questions about a change: did a contract change, did a dependency break the architecture's rules, and did stability shift.

**For Claude Code in planning mode.** Treat this document as the spec, and the HTML mockup of the imaginary repo `smarty/injection` PR #142 as the reference for the visual layer. Plan against the phases in the delivery plan, in order; each phase ends with acceptance checks that can be run. Where this document leaves a choice open, it says so in Risks and open questions; raise those in the plan rather than deciding silently. Keep the tool's own code hexagonal, since it should pass its own checks.

## Problem, goals, non-goals

Agents now produce diffs faster than humans can review them, and line-based review tools rank a 600-line refactor the same as a one-line breaking signature change. Line coverage can read 100% while behavior at the contract goes untested. Agents also sprawl: without an architectural map they read and edit far more of the codebase than a task needs.

**Goals**

1. Rank every change in a diff by contract impact, so reviewers see breaking changes and layer violations first.
2. Enforce the rules of the architecture a team declares in a checked-in config, failing the check on any dependency that breaks them. Five architectures are supported: hexagonal, clean, layered, vertical slices and modular monolith.
3. Render an interactive map that stays legible at 50 modules and 2,000 contract symbols through aggregation and semantic zoom.
4. Export a context slice per symbol or module that gives an agent the contracts it needs and nothing else.
5. Let people design new features in AutoPen, then check the design against the code as it is built.
6. Support languages through one extractor interface, each with a small hand-written contract scanner and no language treated as a special case. Version 1 supports Go, JavaScript, TypeScript and Python.

**Non-goals**

- No AI-generated scores, summaries or risk labels anywhere in the scoring path.
- No tracked outputs. The graph, scores, maps and design files are never committed, and the tool is not a way to hand designs between people.
- Not a UML or C4 replacement and no free-form canvas; every diagram is AutoPen, generated from code or written as a design that is checked against code.
- No runtime tracing; static analysis only. Edges hidden by dependency injection are out of scope.
- Treaty does not choose an architecture. Knowing which one fits the codebase is the team's job; the tool checks the one they declare.
- No strict layering. In the layered architecture a layer may use every layer below it. Whether a team also forbids skipping a layer is a team decision, not a rule the tool enforces.
- No test-strength scoring for now. Contract-scoped mutation testing was deferred on Sep 29, 2026 and may return later.
- Go, JavaScript, TypeScript and Python only in version 1. Other languages follow later, each through its own contract scanner behind the same extractor interface.

## Core concepts

Three terms carry the whole design: layer, contract symbol and change kind. Each is defined so a program can compute it.

**Layers.** Every module has exactly one placement, assigned by path globs in the config: a layer, a slice or context (with a layer inside it when the architecture has one), shared, composition or unclassified. The architecture decides which placements may depend on which; see Architectures. The hexagonal architecture, the default, works like this. A dependency may point inward or sideways, never outward.

| Layer | Rank | May depend on | Expected stability |
| --- | --- | --- | --- |
| Domain | 0 | Domain | Instability near 0 |
| Application (including ports) | 1 | Domain, Application | Middle; port modules abstract |
| Adapter | 2 | Domain, Application | Instability near 1 |
| Composition | 3 | Everything | Nothing may depend on it |
| Unclassified | none | Anything, flagged | Reported as its own finding |

Adapters carry a side, driving (callers such as CLIs and code generators) or driven (infrastructure such as reflection and logging), which sets whether they render on the left or right of the map.

Composition is the composition root: the code that builds concrete adapters and wires them into the core, usually `main`. It must import every adapter, so it cannot live inside the hexagon without breaking the adapter rule. It is the only exception to the adapter rule: it may depend on every layer, adapters included, and a dependency on composition from any other layer is a violation. An adapter built on another adapter is always a violation, even when it is ordinary layered infrastructure; the fix is to move the shared code inward or to wire the two together in composition. On the map, composition shares the left side of the adapter ring with the driving adapters, at its far left, and each composition module has a double outline.

**Contract symbol.** An exported function, method, interface, type or package-level value. A type is part of the contract only if it is exported or appears in an exported signature. Everything else is internal and is scored only through the contracts that reach it.

**Change kinds.** Each symbol in a diff gets exactly one kind, from a signature comparison rather than a text diff.

| Kind | Glyph | Meaning | Default review weight |
| --- | --- | --- | --- |
| Breaking | ! | Signature or method set changed incompatibly | Highest |
| Contract changed | Δ | Signature changed compatibly, such as a new enum value or field | Medium |
| Added | + | New symbol | Medium |
| Implementation only | \~ | Body changed, signature identical | Low |
| Removed | − | Symbol deleted | Treated as breaking if it was exported |
| Moved | → | An exported symbol removed from one place and added in another, as one change | Treated as breaking, unless it moved out of an entry module |

Nothing can import an entry module, such as a Go `main` package, so its removals are not listed, its breaking changes are implementation changes, and a move out of it breaks no one. A breaking change or removal in a private module, one only the repository can import such as a Go `internal` package, is **absorbed** when every dependent changed in the same diff. An absorbed change is still breaking and still fails `fail_on: [breaking]`, but it ranks medium rather than high, since every caller it could break is already in the diff.
| Unchanged | none | No change | Not listed |


## Architectures

*Decided and built Sep 29, 2026.*

Treaty checks one architecture per repository, named by `architecture:` in `treaty.yaml`; without it, the architecture is hexagonal. Choosing the architecture is the team's decision. Treaty checks the declared one and never recommends another.

Every architecture is built from the same few rules, so the engine stays small:

- **Ranked layers.** Layers run innermost, or lowest, first. A layer may depend on itself and every layer inside or below it, never outward or upward. Layering is always relaxed: skipping a layer is allowed.
- **Isolation.** Peers may not depend on each other: hexagonal adapters, vertical slices, and the internals of contexts.
- **Composition.** In every architecture, composition may depend on everything, and nothing may depend on it.
- **Shared code.** In slices and modular, every slice or context may use shared code, and shared code may use only shared code.
- **Public surface.** Another context may use only a context's public packages.
- **No cycles.** Contexts may not depend on each other in a cycle.

| Architecture | Config | Rules | Map |
| --- | --- | --- | --- |
| None (default) | `architecture: none`, or no `treaty.yaml`; nothing else | Every dependency is allowed; contracts and changes are still checked | One region holding every module |
| Hexagonal | `architecture: hexagonal`; `layers:` with `composition`, `domain`, `application` and `adapter` (`driving`, `driven`). A config with layers and no `architecture:` key is hexagonal, as configs were before the key existed | Inward only; no adapter uses another adapter | Rings: domain, application, adapters; driving left, driven right |
| Clean | `architecture: clean`; `layers:` from `entities`, `use_cases`, `interface_adapters` and `frameworks`, in any order; `composition:` | Inward only | Four rings, entities in the center |
| Layered | `architecture: layered`; `layers:` in order, top to bottom, any names; `composition:` | A layer may use itself and every layer below it | Horizontal bands, the top layer highest |
| Vertical slices | `architecture: slices`; `slices:` globs such as `internal/features/*`, each match one slice; `shared:`; `composition:`; optional `layers:` inside every slice, top to bottom, with globs relative to the slice root | A slice may use itself and shared code, never another slice; inside a slice, layers as for layered | A column per slice; with layers, a grid of slices by layer. Composition spans the top, shared code the bottom |
| Modular monolith | `architecture: modular`; `contexts:` globs, each match one context; `public:` globs relative to each context root, default `["."]`; `shared:`; `composition:` | A context may use its own packages, other contexts' public packages and shared code; no cycles between contexts | An island per context; public packages have a bold outline |

For layered, hexagonal and clean, a glob that is exactly a module's path wins over every wildcard, so one package can be placed apart from its directory's glob; otherwise composition is matched first, then the layers in order. A slice or context glob may not contain `**`, because each match must be one directory. A module in a slice that matches none of the slice's layers is unclassified. Settings an architecture does not use, such as `shared:` for layered, are an error rather than ignored, so a mistaken config fails loudly.

Examples:

```yaml
architecture: layered
composition: ["cmd/**"]
layers:            # top to bottom
  presentation: ["internal/web/**"]
  business:     ["internal/service/**"]
  data:         ["internal/store/**"]
```

```yaml
architecture: slices
composition: ["cmd/**"]
shared:      ["internal/platform/**"]
slices:      ["internal/features/*"]
layers:            # optional, inside every slice, top to bottom
  api:   ["."]
  store: ["store/**"]
```

```yaml
architecture: modular
composition: ["cmd/**"]
shared:      ["internal/platform/**"]
contexts:    ["internal/*"]
public:      [".", "api/**"]
rules:
  fail_on: [breaking, layer_violation, cycle]
```

**Proposing a config.** `treaty init --architecture <name>` fits the code to the named architecture. For hexagonal, clean and layered it places modules by the shape of the import graph, so the proposal has no violations. For slices and modular it looks for the directory whose children depend on each other least and makes each child a slice or context; for slices, a child that other children use becomes shared, along with whatever it uses. Outside that directory, programs and modules that use slices are composition, and modules only used by slices are shared. Directory names such as `features` or `modules` only break ties.

**Agents.** `overview` states the architecture's rules in one paragraph, `allowed` names the rule a dependency would break, and a slice's `may_depend_on_layers` lists what the target may use: layer names, or its own slice and shared code, or its context, other contexts' public packages and shared code.

## System architecture

The tool is itself hexagonal: a pure Go core that never calls a parser or git directly, with every side effect behind a port. That keeps the core testable with fakes and lets the tool pass its own layer check, with Go's `internal/` packages enforcing some of the boundaries.

Treaty doesn't need a full parser, only contracts: package clauses, imports, top-level declarations and their signatures, struct fields and interface method sets. Bodies only need scanning for the identifiers that become reference edges. So each language gets a small hand-written contract scanner instead of a general-purpose grammar:

- **A lexer** that knows the language's tokens, comments and string forms. For Go it also inserts semicolons at line ends, so declaration boundaries fall out of the token stream. For JavaScript and TypeScript it marks the first token of each line instead, which stands in for automatic semicolon insertion, tells regular expressions from division and JSX from less-than by what came before, and keeps only the tag names and embedded expressions of JSX.
- **A declaration reader** that walks top-level declarations and renders signatures in AutoPen form. It skips bodies as balanced brackets and never parses an expression or statement.
- **A reference scan** over each declaration's tokens that resolves `pkg.Name` through imports, `recv.Name` through the receiver's type, and bare identifiers within the module.

The language scanners sit together under `internal/adapters/language/`, one package each, and the composition root merges their graphs. JavaScript and TypeScript share one scanner:

- **Modules are directories.** As with Go packages, a module is a directory: `ts` when it holds any TypeScript file and `js` otherwise, so a mixed directory is one module. A directory's `package.json` is its manifest.
- **Contracts are exports.** `export` declarations, export lists, `export default`, and CommonJS `module.exports` and `exports.name` assignments make contracts. A declaration exported by a later list gets `export` written ahead of its signature, so a signature alone says whether it is a contract. Class members are contracts unless `private` or `#`-named.
- **Names are scoped to a file, symbols to a directory.** A top-level name declared in more than one file of a directory is qualified by its file's stem, as in `button/render`, so neither hides the other. Overloads and accessor pairs within one file are variants of one symbol.
- **Imports resolve inside the tree only.** Relative paths, with TypeScript's `.js`-to-`.ts` mapping and index files; `paths` and `baseUrl` from the nearest `tsconfig.json` or `jsconfig.json` (its `extends` is not followed); and packages in the tree by their `package.json` name, through `source`, `exports`, `module` or `main`, then an index or `src/index`. Imports and re-exports, including barrels, resolve to the declaring symbol. Anything else, such as `node_modules`, is outside the repository.
- **What is skipped.** Declaration files (`.d.ts`), tests (`.test.`, `.spec.`, `__tests__`), minified files, and `node_modules`, `dist`, `build` and `coverage` directories.
- **Breaking changes** compare parameter types, optional and rest markers, and result types, ignoring parameter names; a parameter without a type compares as `_`. Constant values may change.

Python has a scanner of its own, built the same way:

- **The lexer** joins lines inside brackets and after a backslash into logical lines and records each one's indentation, which is how blocks are read. It knows prefixed and triple-quoted strings, and lexes an f-string's replacement fields as code.
- **Modules are directories**, `py`, like packages. A directory's `pyproject.toml`, `setup.py` or `setup.cfg` is its manifest; a directory with `__main__.py`, or a file that tests `__name__ == "__main__"`, is an entry point; a directory whose path has an `_`-prefixed part is private.
- **Contracts are public names**: those not starting with an underscore, plus special names such as `__init__`. `__all__` decides only what `from module import *` brings in. Signatures are the `def` and `class` lines without their colons, and `name: type = value` for module variables. A class's fields are the attributes its body assigns or annotates and those `__init__` assigns to `self`. A class with `Protocol` among its bases is an interface; every base becomes an embeds edge.
- **Definitions inside `if`, `try` and `with` blocks** are read as top level, since that is where conditional imports and definitions live. Imports at any depth are dependencies.
- **Imports resolve inside the tree only**: relative imports from the file's package, and absolute ones from the source roots, which are the repository root, `src`, and each project directory and its `src`. A package found in no root but in exactly one place in the tree, as a vendored dependency is, resolves there. `a.b.c.name` walks packages and submodules to the symbol. As with JavaScript, a name defined in more than one file of a directory is qualified by its file's stem.
- **What is skipped.** Tests (`test_*.py`, `*_test.py`, `conftest.py`, `tests` directories), stubs (`.pyi`), virtual environments, caches, and `build` and `dist` directories.
- **Breaking changes** compare annotations, defaults, `*`, `**`, `/` and `*` markers and the return annotation, ignoring parameter names and a method's `self` or `cls`. Variable values may change.

The same scanner reads AutoPen declaration lines, so design signatures and code signatures are compared by one piece of code. The tool is pure Go with no cgo: it cross-compiles, and `go install` needs no C toolchain. On this repository, the Go scanner produced the same modules, symbols, signatures and fields as a tree-sitter-based extractor it replaced, in about a twentieth of the time.

&#91;embedded content: tool architecture · core, ports and adapters\]

A run moves through these steps in order:

1. The VersionControl adapter materializes the base and head trees.
2. The SourceExtractor builds a graph for each tree: modules, symbols, signatures and reference edges. Each language's extractor reads the whole tree and their graphs are merged. Sub-trees with their own `go.mod` are part of the tree; each resolves imports through its own module path.
3. The config assigns each module a layer; modules that match no glob become Unclassified.
4. The domain classifies each symbol's change kind by comparing base and head signatures.
5. The domain checks layer rules and computes stability metrics on both graphs.
6. The application ranks the review queue and writes the map, report and slices through the Output port.

## Graph data model

One graph per tree, with three node kinds and two edge kinds; everything else is derived. The graph lives only in memory and is never written to a file. The renderer, the check report and the agent slices all read the same in-memory graph in the run that built it.

| Entity | Key fields | Notes |
| --- | --- | --- |
| Module | `id`, `path`, `name`, `layer`, `side`, `slice`, `public`, `files[]` | `name` is what the language calls it, such as the Go package name; `side` is `driving` or `driven` for hexagonal adapters, empty otherwise; `slice` is the root path of the vertical slice or context holding the module; `public` marks a context's public packages |
| Symbol | `id` (module-qualified name), `module`, `file`, `line`, `kind`, `contract`, `signature`, `variants[]` | `kind` is function, method, interface, type or value; each variant is another declaration's file, line, signature and fields |
| File | `path`, `module`, `symbols[]` | Only needed for the inspector and diff attribution |
| Reference edge | `from`, `to`, `kind`, `file`, `line` | `kind` is call, type-use, implements or embeds |
| Containment edge | `module`, `symbol` | Implicit in `Symbol.module`; stored only in the rendered form |

A diff result, also held in memory, pairs two graphs and adds, per symbol, `change` (a change kind) and `before_signature`, plus the review findings that apply to it. Per module it adds before and after values for afferent coupling, efferent coupling, instability, abstractness and distance. Per module edge it adds `new`, `violation` and the underlying references.

Symbol ids must be stable across renames of unrelated code, since agent slices and design files name symbols by id. Use `<language>:<module path>:<qualified name>`, such as `go:injection:Container.Resolve`, `go:internal/graph:Graph.Order` or `c:include/injection.h:inj_resolve`, and never a line number. The module path, not the package name, keeps ids unique when two packages share a name. Each module also records its `language`.

Each function also carries a hash of its normalized body. It is not part of any id or reference; it only tells an implementation-only change from an unchanged symbol.

## Diagram language (AutoPen)

AutoPen is the language of the graph. For code it exists only in memory: the extractors build it, the scoring reads it, the renderer draws it, and the run ends without writing it anywhere. It is structure only. Change kinds, metrics and findings are computed on demand and kept in memory beside the graph, never inside it.

The only AutoPen text on disk is design files. A person writes one to sketch a feature before the code exists, and the tool checks it against the code as it is built. So the text syntax is shaped for writing by hand, not for generated output or git diffs. Rough edges are expected and will be fixed as the language gets used.

**What AutoPen must express.** Modules with path and language; contract symbols with kind and signature; dependencies between modules and symbols; design elements that have no code yet; expectations, such as a maximum instability; and forbidden dependencies. Layers and adapter sides are not written in AutoPen. They always come from `treaty.yaml`.

**Decisions.**

| Decision | Choice | Why |
| --- | --- | --- |
| Base syntax | Paths as headers, each language's own declarations beneath, scoped by indentation | Code reads the way it is written, and nesting shows ownership. A small hand-written parser needs no dependencies. |
| Signature notation | Each language's own syntax, without bodies or receivers | A neutral notation loses information, and breaking-change rules are per language anyway. Receivers are detail below the level of a contract map. |
| Design and generated content | Only design files exist on disk | Generated AutoPen never leaves memory. A design names existing code by symbol id, resolved against the freshly built graph on every run. |
| References | Symbol ids only | Every graph is built from the tree it describes, so nothing can go stale. |
| Expectations | A small fixed set of `@` directives | Every check is already a short, closed list. An expression language would add an evaluator to the core. |

**Shape.**

- The first line is `autopen 1`. A design's second line is `design "<title>"`.
- Scope comes from indentation: exactly two spaces per level, and tabs are an error.
- A line at column zero is a header: `<lang>:<path>`. A path ending in a source file extension names a file, such as `go:adapters/database/DB.go`. Any other path names a module, such as `go:injection`, for designs where no file has been chosen yet. A file's module is derived from it: for Go, its directory.
- An indented line is a declaration in the header's language, one per line, without a body. Nesting shows ownership: methods and fields sit under their type. In Go, a nested line starting with `func` is a method and any other nested line is a field.
- A line starting with `@` is a directive, not code. No supported language starts a declaration with `@`. TypeScript decorators are not part of a signature, so they never appear.
- `//` at the start of a line is a comment.
- AutoPen's parser reads only headers, indentation and directives. The text of each declaration goes to that language's snippet parser.

Whether a symbol is part of the contract comes from the native syntax: in Go, an exported name, and for a method, an exported method on an exported type. Symbol ids are unchanged, `<lang>:<module path>:<qualified name>`, so the method below is `go:adapters/database:MySQL.Create` whichever file it lives in.

**Directives.**

| Directive | Placed under | Meaning |
| --- | --- | --- |
| `@expect instability <= N` or `>= N` | header | Martin's I |
| `@expect abstractness >= N` or `<= N` | header | Martin's A |
| `@expect distance <= N` | header | Martin's D |
| `@depends <target>` | header, declaration | Intended dependency; checked against layer rules immediately, before any code exists |
| `@forbid <target>` | header, declaration | No dependency on the target; globs allowed |

**Examples.** A design, `.treaty/designs/scoped-lifetimes.pen`:

```
autopen 1
design "Scoped lifetimes"

go:injection
  type Container interface
    func Resolve(ctx context.Context, k Key) (any, error)
    func Scope() scope.Scope
  const Scoped Lifetime = 2

go:injection/scope/scope.go
  @forbid go:injection/ports
  @expect instability >= 0.50
  type Scope interface
    func Resolve(ctx context.Context, k injection.Key) (any, error)
    func Dispose() error
  func New(parent injection.Container) Scope
    @depends go:injection:Container
```

Structs carry fields and methods at the same level, and struct tags need no escaping:

```
go:adapters/database/DB.go
  type DB interface
    func Create(data DTO) bool
  type MySQL struct
    Addr string `json:"addr"`
    func Create(data DTO) bool
```

**Designing in AutoPen.** A design is partial. It lists only what it adds or pins down, and code it doesn't mention is ignored. Whether an element is new isn't written down; the tool works it out against the current graph. A file in a header is a hint: symbols are matched by id, so moving code between files never counts as drift.

**Starting a design.** `treaty design new <name> [--from <module or symbol>...]` writes `.treaty/designs/<name>.pen`. It contains the header, then for each `--from` target the file headers and contract declarations from today's code. Internal symbols, locations and edges are left out. With no `--from`, the file has only the header. The command refuses to overwrite an existing design.

The person then edits declarations, adds headers and declarations, and adds directives. Scaffolded lines left unedited stay in the design and pin those signatures: `design check` reports if the code drifts from them. Deleting a line drops that pin. The map is display-only for designs; it draws them with `treaty map --design` but never edits them.

**Signature comparison.** Each language's scanner also reads AutoPen declaration lines, using the same code that reads source, so a design signature and a built signature go through one parser. Signatures are compared by structure, ignoring parameter names: `func Dispose(ctx context.Context) error` matches `func Dispose(c context.Context) (err error)`. Receivers are never written. The graph still records whether a Go method has a pointer or value receiver, the contract diff still reports a change as breaking, and the inspector shows it.

**Checking a design.** The map draws design elements with a distinct outline next to the real code. `treaty design check` compares a design with the current code and reports:

- designed contracts not yet built;
- built contracts whose signatures differ from the design;
- expectations that fail;
- designed dependencies that break layer rules;
- designed modules that `treaty.yaml` leaves Unclassified.

The same design block becomes the brief for an agent implementing it.

**Printing the graph.** `treaty dump` prints the in-memory graph to stdout in the same syntax, with one file header per source file. It adds what a design leaves out:

- internal declarations;
- a trailing `@<line>` on each declaration, and `@ptr` on Go methods with pointer receivers;
- reference edges nested under their source as `@call`, `@type-use`, `@implements` or `@embeds` lines, each with a target id and a location: `@<line>`, or `@<file>:<line>` when the edge was found in another file, such as a build variant;
- `@variant <file>:<line>` under a symbol for each build variant, and for each repeat of a name the language allows more than once, such as Go's `init`;
- `@import <module> @<line>` under a file header for each import of another module in the repository.

Design files may not use these. Output is canonical: headers are sorted by path, declarations follow source order, and edges are sorted by kind, target and location. It exists for debugging and golden tests. Parsing it back must yield the same graph, which proves that the snippet parsers and the extractors share one model.

```
go:adapters/reflectx/name.go
  func TypeName(t reflect.Type) string @7

go:internal/graph/names.go
  func typeName(t reflect.Type) string @9
    @call go:adapters/reflectx:TypeName @11
```

## Mechanical scoring

Four checks run on every diff, and each one is reproducible from the two trees and the config alone. Same inputs, same numbers.

**1. Contract diff.** Compare base and head signatures from each language's extractor, not text. Each language supplies its own breaking-change rules. Go follows the rules of `apidiff`: removed exports, changed parameters or results, and new methods on exported interfaces are breaking; renamed parameters, added struct fields and changed constant values are compatible.

A removed exported symbol pairs with an added one, and becomes a single moved change, when exactly one candidate matches on either side: the same qualified name in a different module, or the same module and signature under a different name. The methods and fields of a moved type fold into the type's change when their signatures are unchanged. When a type's fields change, the change lists the field lines removed and added.

A symbol declared more than once in a module, in files built under different constraints such as Go build tags, is one symbol with variants. It keeps the first declaration's location and signature, its reference edges are the union of every variant's, each carrying its own file, and its body hash covers every variant, so a change to one platform's code alone still shows. Variants whose signatures or fields disagree are reported as a finding.

**2. Architecture rules.** A module depends on another when it references one of its symbols or imports it at all, so a blank import, a dot import, a package-level `var _ = …` and code that runs only in `init` all count. A module edge is a violation when it breaks a rule of the declared architecture, and each violation carries the rule it breaks in words. In every architecture, composition may depend on anything and any edge into composition is a violation. For the modular monolith, an edge between two contexts that depend on each other, directly or through other contexts, is also a cycle finding, unless the edge already breaks a rule. Violations and cycles fail the check by default. Unclassified modules produce a warning, not a failure.

**3. Stability metrics.** Robert Martin's package metrics, computed at module level on both graphs, with afferent coupling Ca and efferent coupling Ce counted as distinct modules:

```latex
I = \frac{C_e}{C_a + C_e} \qquad A = \frac{\text{exported interfaces}}{\text{contract symbols}} \qquad D = \lvert A + I - 1 \rvert
```

The report flags a domain module whose instability rises, and an adapter that other modules depend on.

**4. Review ranking.** Each finding gets a severity from its kind, and the queue sorts by severity, then by blast radius (count of contracts that transitively depend on the symbol).

| Finding | Severity |
| --- | --- |
| Breaking contract change | High |
| Layer violation | High |
| Interface change that breaks implementers | Medium |
| New module or new contract symbols | Medium |
| Compatible contract change | Low |
| Implementation-only changes, grouped | Low |

Which findings fail the check and which only warn is set in the config.

## Visual design

The HTML mockup is the reference: three panels, with the review queue on the left, the hexagon map in the middle and the inspector on the right. The same page runs two ways. Served live, it loads the view from the server and redraws on every rebuild (see Live map and MCP server). Written by `treaty map`, it is a single self-contained HTML file with the view embedded and no server. Either way the view embeds the source span of each symbol it shows, so the inspector can display code without reaching the repository.

**Map.** The layout follows the architecture (see Architectures). For hexagonal, three concentric flat-top hexagons: domain in the center, application in the middle ring, adapters outside. Driving adapters sit on the left and driven adapters on the right, with composition at the far left of the adapter ring. A module is labelled with its path; the repository root, whose path is `.`, is labelled with the name its language gives it, such as its Go package name. Each module is a hexagon inside its ring or region, holding one cell per file, spaced evenly around a ring in file-name order and leaving the middle empty; a module with one file has its cell in the middle. Every cell has the same size across the map, so a module with more files is larger. A cell's shade darkens with its symbol count relative to the most crowded file, and hovering it names the file and counts its symbols and contracts.

The border stays the module's contract surface at every zoom, and every symbol is drawn once. Each file's contracts sit on the stretch of border facing its cell, marked by a bracket just inside the border, so position ties a contract to its file without lines or color. Inside, each cell holds the file's internals, drawn as their kind (function, method, interface, type or value) once a cell is about 20 pixels across, so crowding shows as a dense cell; "Show internals" or a selection that touches them shows them before that. Pointing at a cell, a bracket or a contract lights up that file's cell, bracket and contracts together, and a selected symbol keeps its file lit; nothing stays lit otherwise. Planned contracts, which have no file yet, wait in the empty middle. Its contract symbols sit evenly spaced on the module's border, and internal symbols appear inside only when the "Show internals" toggle is on or a selection touches them.

**Encoding.** Nothing relies on color alone; every color has a paired shape or glyph.

| Channel | Encodes | Values |
| --- | --- | --- |
| Node shape and fill | Symbol kind | Hollow circle function, filled circle method, hollow square interface, filled square concrete type, filled triangle value |
| Color and glyph | Change kind | Grey unchanged; green with + added; amber with Δ compatible, ! breaking, → moved or \~ implementation only; dashed in the planned color for unimplemented. In signature diffs, removed text is red and added text green, as in git; the color-blind safe themes keep blue for added and orange for removed. Hollow shapes take the color on their outline, filled shapes in their fill; the glyph sits beside the icon |
| Module border | New module; composition; public API | Dashed blue; a double outline for composition; a bold outer outline for a context's public packages |
| Teal and dashed | Unimplemented: designed or planned, not yet built | Teal (`#0d9488` light, `#2dd4bf` dark) on the symbol or module outline, always with a dashed outline so the state doesn't rely on color. It turns blue with + once built |
| Module edge | Dependency | Width grows with reference count; blue when new; amber when changed: it gained or lost a reference, or the signature of a symbol one of its references points to changed. A change to code alone does not count |
| Dashed edge with hollow arrowhead | Implements or embeds | Drawn separately from solid "uses" edges; a module pair with both bows the two curves apart |
| Magenta edge with ! marker | Rule violation or context cycle | Always drawn, even with module edges hidden; its label and the inspector give the rule it breaks |

**Aggregation.** By default, edges are drawn between modules only. Selecting a symbol draws its symbol-level edges and labels, and dims everything unrelated. The mockup's hand-placed layout does not scale: at more than about 12 modules per ring, module positions and edge routing must be computed, with edges bundled along the gaps between rings.

**Groups.** Within each ring, a directory with two or more packages or sub-directories becomes a group: a hexagon drawn around them, nested as deep as the directories go. A directory with a single child is skipped, a package with sub-packages is a group whose center is the package itself, and a group that would hold a ring's entire contents is unwrapped onto the ring. Groups are visual only: layer rules, metrics and slices stay per package. The layout is computed bottom-up for the fully expanded tree, so collapsing a group never moves anything. A group collapses on its own when it is small on screen, showing its name and counts, and edges attach to it instead of the packages inside; double-clicking it or using *Go to* on something inside it expands it. Selecting never moves the view or expands a group: a selection hidden in a collapsed group lights that group, and the inspector says where the selection is and offers *Go to*.

**Themes.** Every color on the page is a named token, named for what it means rather than its hue: surfaces such as `bg`, `panel`, `ink` and the ring tones, and signals such as `added`, `removed`, `changed`, `violation`, `planned`, `selection`, `claude`, `accent` and the three severities. A theme is a JSON file that sets every token, with a `name`, a `base` of light or dark, and a `group`. Treaty ships 18, in three groups:

- **Standard:** Light and Dark, after VS Code's default Light Modern and Dark Modern: neutral surfaces, its text grays and focus blue, and its own meanings for signals (git's added green, modified amber, error red, warning yellow).
- **Accessibility:** High Contrast Light and Dark, and Color-blind Safe Light and Dark. Color blindness is treated as an accessibility need met by its own themes, not a switch on every theme.
- **Style:** Dawn and Dusk (muted, low-strain), Fjord (frosty blues), Vampire (purple with neon), Harvest (warm earth tones), Blossom (muted pinks), Great Wave (ink blue and sunset, after Hokusai), Gumdrop and Gumdrop Light (pastels), Neon Rain (deep navy with neon), Comic (flat primaries with black outlines), Midnight (near-black navy), DOS (dark) and DOS (light) (only the 16 text-mode colors, as a black or a white terminal), and Windows 3.x (button-face gray, navy highlights and a teal desktop, from the 16-color VGA palette).

Treaty writes its themes to `~/.treaty/themes/` every time a server starts, each with a warning that it is rewritten and any change to it will be lost at any time without warning; `.defaults.json` in that folder records which files are Treaty's, so any other `.json` file there is a person's own theme, listed under Yours, picked up automatically and never touched. A default that no longer ships is removed. The header's Theme menu offers System, which follows the operating system between Light and Dark, and every theme by group; a theme file that cannot be read is reported in the header and left out. The legend shows colors as swatches of the live tokens, never by name.

A test holds every default theme to the map's meaning: the five signals stay at least 15 apart in CIEDE2000, the severities at least 12, text meets WCAG contrast (4.5, or 7 for high contrast) and selection labels stay readable. Removed, which shows only in signature diffs, must read as text and stay at least 15 apart from added. Every theme except the color-blind safe ones uses the conventional green for added and red for removed, so planned and Claude take other hues there. The color-blind safe themes also keep added, changed, violation and planned, and added and removed, at least 20 apart when simulated for protanopia, deuteranopia and tritanopia (Machado 2009); no set of five hues managed that, so in them selection glows in the ink color instead.

**Panels and drawers.** The map, the review queue and the inspector are panels, each a tab in a stack. Stacks sit in drawers on the left, right and bottom of the map, in the center with the map, or in floating windows above it; by default the queue is on the left and the inspector on the right. Dragging the divider between a drawer and the map resizes the drawer, and dragging between two stacks in a drawer shares its room between them. Dragging a tab moves its panel: dropped on a tab bar it goes among that bar's tabs where a line shows, which also reorders a stack's own tabs; dropped on a stack's middle it joins that stack; on the near half of a stack in a drawer it splits the drawer there; at the workspace's left, right or bottom edge it docks in that drawer; anywhere else it floats, and a floating window moves by its tab bar and resizes from its corner. The map stays in the center. The map's views, References, Files and Symbols, reorder the same way within their bar but cannot dock, and their order is kept with the layout. An empty drawer takes no room. *Reset layout* restores the default.

**Preferences.** A person's theme, panel layout and "Follow Claude" choice are kept in `~/.treaty/settings.json`, so they follow the person across repositories, sessions, browsers and ports. The live page loads them when it opens and saves each change through the server, in batches, merging only the fields that changed; it sends nothing until the saved preferences have loaded, so the layout a page starts with never overwrites the saved one. The first time a person's browser meets a server with nothing saved, the choices already in that browser become the saved ones. The file is replaced atomically, so two servers saving at once never tear it. A static map, opened with no server, keeps its choices in the browser. New panels, such as the Tests tab, join the same system, so each person can put them where they like.

**Tests and coverage.** The live map has a Tests tab, docked beside the inspector and the Code tab. With nothing selected it lists every test as a file tree: directories, each package's tests, and the subtests a run reported, each opening and closing. Selecting a symbol shows only the tests that use it or its members; a file, those that use its symbols or sit in its `_test` file; a module or group, those in it or using it. A test uses what it names and what the helpers in its own test package name, transitively, and a test named for a symbol, such as `TestService_Map`, targets it. Each row shows its outcome as a glyph and a color, passed, failed, skipped, running or queued, and a failed test opens its output. Run runs every test shown, and each row's ▶ runs that test, subtest, package or directory alone. Version 1 runs Go tests, the `Test` and `Fuzz` functions of `_test.go` files, with `go test -json -coverprofile`: modules run whole share one `go test` per Go module, and named tests run one per package. Coverage is per line, and every run's coverage of a file adds to the earlier runs' until the file changes, when it is dropped. It draws as a ring around an icon: a faint track and an arc from 12 o'clock clockwise, so half covered is the right half. Symbols carry the ring on every map view; the Tests tab rings the selected symbol, file or module, and lists the coverage of a file's symbols or a module's files. A type's coverage includes its methods. Code no run has covered has no ring. A static map has no Tests tab content, since nothing can run.

**The symbols view.** Besides references and files, the map has a symbols view that reveals what a caller can reach, the way code completion does after a dot. It starts at the repository's root, named by its package, such as a Go package name. A package opens to its subpackages and then its contract symbols, and a type opens to its members, each choice opening a column to its right. Packages code outside the repository cannot import are left out: Go internal and main packages, Python packages whose path has an `_`-prefixed part or that hold only scripts, and JavaScript packages under a private `package.json`. They return, with internals, when "Show internals" is on.

**Selecting files.** A file cell is selectable like a symbol or module. Selecting one keeps its cell, bracket and contracts lit, dims what it does not touch, and shows in the inspector its path, its symbol, contract and internal counts, the files and modules it uses and is used by, and chips for its contracts and internals. For agents, a file's selection id is its module's id and its path joined by `|`, with the module's slice.

**Manifests.** A package holding its language's module file, such as `go.mod`, `package.json` or `pyproject.toml`, is where a module begins. The file takes the first cell of the package's ring, at 12:00, ahead of the source files, and names itself instead of being shaded, since it declares nothing. Selecting it shows the file in the inspector.

**Changes since the baseline.** Against a baseline, a changed symbol's inspector shows its signature as a removed line and an added line, and its code as a line diff with the baseline, removed lines red and added lines green, for implementation changes as well as contract changes. What the baseline had and the working tree does not stays on the map in red with a dashed outline where it was: removed modules, removed files as cells of their module, removed symbols in those cells or their file's cell, removed module dependencies as dashed red arrows, and, when a symbol is selected, the references it lost. A removed symbol's inspector shows its old code, all red. A symbol that moved shows only where it went.

**Moving modules.** Pressing a module and holding still for half a second picks it up; moving first pans, as before. While it is carried, the band, ring or area under the pointer lights up, its edges fade, and the tip says what a drop will do; Escape puts it back. Dropped in its own band, ring or area, the module stays where it was put: the position is saved per repository and architecture in `.treaty/positions.json`, is honored only while it still lies in the module's band, and grows the module's groups to keep it inside. Dropped in another layer of a layered, hexagonal or clean architecture, including a side of the adapter ring or the composition arc, the server edits `treaty.yaml` through its YAML tree, keeping comments: it removes the module's exact path from every list and adds it to the target's, unless the wildcards already place it there. It then rebuilds, so the rules, checks and agents follow at once. Moves between layers are refused while another architecture is previewed, and for slices and modular, whose areas come from directories.

**Review queue.** Findings sorted as described under Mechanical scoring. Each item shows a severity icon, a title and one sentence, and selecting it selects the related symbol, module or edge on the map.

**Inspector.** Its content depends on the selection:

- **Symbol:** qualified name, kind, file, change kind, a line diff of the signature, and clickable dependency and caller chips.
- **Module:** files with change status, the five stability metrics before and after, the guidance for its layer, and its contract chips.
- **Module edge:** the rule, whether it passes, and each underlying reference with file and line.

Every inspector view ends with the agent context slice for that selection and a copy button, collapsed. It opens only when the person opens it and stays open until the selection changes; the choice is never saved. Selecting an arrow lists its references as a table of source, reference and destination, each row colored added, removed, or changed when the destination's signature changed since the base; clicking a source or destination selects that symbol. The page supports light and dark themes, keyboard selection of every node, and respects reduced-motion settings.

## Live map and MCP server

*Decided and built Sep 29, 2026.*

The map stops being a static page that has to be regenerated. A long-running server holds the graph in memory, watches the working tree, re-extracts it when files change, and pushes each update to every open browser with server-sent events. Each event carries the new version, baseline and any build error, and the page then fetches the view, which avoids a second protocol and needs nothing beyond the standard library. The server listens on 127.0.0.1 only, always on port 7878, so every session and browser finds it in the same place; it refuses requests addressed to any other host name, which blocks DNS rebinding, and it accepts writes only as JSON, which a web page on another origin cannot send without a preflight the server never grants. The same process serves the MCP tools, so the person in the browser and the agent in Claude Code always look at the same graph. One server runs per machine. It keeps a project for each repository a session works in. The server's one page, at its root, has a tab per open project, named for the folder, with its parent added when two open folders share a name; `/?p=<folder>` opens with that project's tab shown, which is the link sessions hand out. Each tab shows its project's map, served at `/p/<folder>/`, in a frame that loads the first time the tab is shown and stays loaded while the project is open, so switching back keeps the view. The tabs follow projects as sessions open and close them. They sit in the order their projects opened until the person drags them into another order, shown by the same insertion line as the map's tabs; the browser remembers it, and a project opened later goes last. Project tabs reorder but never dock. A project that opens later is marked until it is shown, and a hidden project whose agent points at something is marked in Claude's color. A project's map opened on its own moves into the page, so one browser tab holds every project.

**Starting and stopping it.**

- **From a session.** Claude Code starts `treaty mcp` over stdio as an MCP server. That process is a shim. It opens an attach stream to the server, an HTTP upgrade that then carries the session's newline-delimited JSON-RPC in both directions, and relays the session over it. If no server answers, the shim starts one apart from itself (in its own process session, logging to `~/.treaty/daemon.log`). When several shims start one at once, the first to bind the port serves and the rest exit. The first session in a repository opens its project. Later sessions there join it, so a second Claude Code in the same folder adds no second map.
- **On its own.** A person runs `treaty serve [--open]`, to review a pull request or explore a codebase with no agent involved. It attaches like a session that sends nothing, so it keeps the map open until it is interrupted.
- **Lifetime.** The attach stream is how the server knows who is connected: when a shim exits, however it exits, its connection closes and its session leaves. A project closes 30 seconds after its last session leaves, and the server stops 30 seconds after its last project closes. The grace lets a session that reconnects keep its map.
- **Restarts.** The server stops when someone runs `treaty restart`, or when the `treaty` executable is replaced, as `go install` does. Each shim notices its stream close. It reconnects within 15 seconds, starting the new server if needed, sends again every request still unanswered, and sends `notifications/tools/list_changed` so the agent reads the tool list again. A request that waits longer than that is answered with an error telling the agent to try again or to reconnect with `/mcp`. The shim only relays bytes, so a shim from an older build keeps working with a newer server. The attach stream carries a protocol number; when a shim and a server disagree on it, the server refuses the shim, which stops that server and starts its own. The browser reopens its event stream until the restarted server has rebuilt the project. The baseline, selection and which violations the agent has been told about live in memory, so a restart resets them.

**Finding the map.** Claude Code does not show an MCP server's output to the person, only to its logs, so the link reaches them through the agent: the server's MCP instructions give the agent the URL, say whether the map is new or the one already running, and ask it to share the link in its first reply. The `overview` result starts with the link too, and `treaty url` prints it on demand.

Either way it writes nothing tracked, and its state disappears when it stops. The static `treaty map` output stays available for a one-off snapshot.

**Baseline.** Changes are always shown against a baseline, and the person can change the baseline from the map's header while the server runs, without restarting it. There are three modes:

- **HEAD**, the default, which shows uncommitted work.
- **Pull request**, for code review: the merge base of HEAD and the target branch, so that every commit on the branch counts, however many there are. The target defaults to the remote's default branch (`origin/HEAD`, else `origin/main`, `origin/master`, `main` or `master`), and the person can type another.
- **Ref**, any revision, such as `HEAD~3`.

The server re-resolves the baseline every two seconds, so a commit moves a HEAD baseline forward. Each baseline tree is extracted once and cached by commit.

**Watching.** The server fingerprints the path, size and modification time of every file outside hidden, vendor and dependency directories, plus `treaty.yaml` and the designs, and rebuilds when the fingerprint changes. How often it looks depends on the repository's size: twice a second for small and medium repositories, and once every five seconds for large ones, those with 20,000 files or more or whose walk takes 250 ms or longer, so watching never becomes the work. A failed build, such as a config that does not parse mid-edit, keeps the last good map and shows the error in the header.

**Architecture view.** A dropdown in the header switches the architecture the map draws. Choosing one other than `treaty.yaml`'s previews it: the map shows the code fitted to that architecture, exactly as `treaty init --architecture` would propose it, with that architecture's layout and violations. Checks and every agent tool keep following `treaty.yaml` during a preview. The preview replaces `treaty.yaml` with that proposal when the person presses *Use this architecture*, or once it has been left in place for five minutes; a countdown in the header shows how long is left, and choosing `treaty.yaml`'s architecture again cancels it. The previous `treaty.yaml` is overwritten, not kept: an architecture is chosen rarely, usually at the start of greenfield work, and git holds the old file.

**Selection and pointing.** The agent reads what the person has selected on the map with `selection`, so the person can click a module and say "work here." The agent can also ask the person to look at something with `show`, but it never takes the view from them:

- **An offer, not a move.** By default `show` does not move the view or change the selection. The item pulses in teal, and a notice in the corner of the map gives the agent's reason with *Go* and *Dismiss*. The view moves only when the person clicks *Go*. An unanswered notice fades after 20 seconds, and a new one replaces it rather than stacking.
- **Follow Claude.** A switch in the header, off by default and remembered per browser, lets `show` move the view, but only after the person has left the map alone for 4 seconds. While they are using it, a request is an offer as usual.
- **Rate limit.** The server accepts one `show` every 15 seconds. A request sooner is refused with how long to wait, so the agent learns to hold back; a session that follows another server forwards its requests there, so the limit covers every session.
- **Guidance.** The tool's description tells the agent to use it only when it needs the person's eyes on one specific thing, such as before asking about it or when something needs a decision, and never to narrate progress.

**Layout under change.** Modules may move as others are added or removed; positions are not frozen to a baseline. The view is anchored to the current selection instead. If a selected module sits in the lower left of the screen and a re-layout moves it, the view pans and zooms with it, so it stays where the person left it. With nothing selected, the view keeps its center and zoom.

**MCP tools.** Every tool answers from the in-memory graph, in compact AutoPen where possible, so an agent can orient itself cheaply:

| Tool | Answers |
| --- | --- |
| `overview` | The whole architecture in a few kilobytes: modules by layer, contract counts, module edges. The agent's first call, instead of exploring files |
| `find` | Symbols and struct fields by name or kind, with id, file, line and signature; a field shows its type's location |
| `slice` | The context for working on one symbol, file or module, as compact text by default (exists) |
| `source` | The numbered lines of a symbol, from its documentation, or of a file's range, so an agent reads only what a slice points to. A file or range of more than 120 lines that is most of its file gives the file's outline instead, unless the agent asks for all of it |
| `impact` | What depends on a symbol, transitively: its blast radius, before an edit |
| `allowed` | Whether one module may depend on another, before an import is added. Modules may be named by id, path or short name, or by a symbol or file in them; only a path that matches nothing is treated as a module not built yet |
| `changes` | What changed in the architecture since the baseline: contract changes, new and removed edges, new violations. The agent checks its own edits with this |
| `plan` | Writes unimplemented AutoPen: the contracts and dependencies the agent intends to build, in design syntax. It is validated like a design, so it reports layer problems and conflicts with existing code, and it is overlaid on the live map as unimplemented. As the code is written, each item turns into a built one, so the person can watch the plan being filled in. A plan is saved as a design file in `.treaty/designs/`, so it survives a restart, `design check` works on it, and the person can open and edit it |
| `selection` | What the person has selected on the map |
| `show` | Asks the person to look at one symbol or module: it pulses and a notice offers to jump there. At most once every 15 seconds |
| `check`, `design_check` | As today (exist); `check` summarizes by default, with `format: json` for the full report |

Every tool that takes a target accepts an id, a file, or a short name that matches exactly one symbol, module or file: a qualified name, a method's own name, or the tail of an id or path. An ambiguous name is refused with its candidates, and an unknown one with near matches.

**Nudging.** The server tells the agent about violations without being asked. Every tool result ends with a warning listing each layer violation in the code that this session's agent has not been told about yet, such as one its last edit introduced, with the rule and the first offending reference, and asks it to fix the violation or explain it to the person. `overview`, `changes` and `check` list violations themselves, so they only mark them told. A violation that is fixed and later returns is news again.

## Agent context slice

A slice is the minimum an agent needs to work on one symbol or module without reading the rest of the codebase: the target's contract, its neighbors' contracts, and the layer rules it must not break. Implementations outside the target module are never included.

Example slice for `go:injection:Container.Resolve` in the fixture, as `treaty slice --format json` prints it. Against a baseline, `contract` also carries the change, such as `"change": "breaking"`:

```json
{
  "schema": "treaty/slice/v1",
  "task_scope": {
    "symbol": "go:injection:Container.Resolve",
    "file": "injection/container.go",
    "line": 15,
    "end_line": 15,
    "layer": "application",
    "kind": "method"
  },
  "contract": {
    "signature": "func Resolve(ctx context.Context, k Key) (any, error)"
  },
  "may_depend_on_layers": ["domain", "application"],
  "neighbors": [
    {"symbol": "go:injection:Key", "relation": "uses", "signature": "type Key struct", "file": "injection/container.go", "line": 9}
  ],
  "excluded": {"modules": 5, "symbols": 19}
}
```

Rules for building a slice:

- Neighbors are direct uses and direct callers only; the agent can request a wider slice explicitly.
- A neighbor in the same module includes its signature; one in another module includes its signature only if it is a contract symbol.
- `excluded` counts what was left out, so the agent knows the slice is partial rather than complete.
- A module slice lists the module's contract, its allowed layers, and which modules depend on it.
- A violation slice lists the rule broken and the offending references, so a fix can be verified by rerunning the check.

Slices are regenerated from the graph on each request and never edited by hand.

## Interfaces

The tool ships as a single binary, `treaty`, driven by a config file at the repository root. The one-shot commands below build the graph fresh on each call. The live server, which also serves MCP, is described in Live map and MCP server; `treaty serve` and `treaty mcp` start it when it is not running. Everything the tool writes goes under `.treaty/` at the repository root, which ignores itself.

| Command | Does | Output |
| --- | --- | --- |
| `treaty -C <dir> <command>` | Runs any command on the repository at `dir`, as `git -C` does, so an agent or `.mcp.json` elsewhere can use it | As the command |
| `treaty check [--base <ref>] [--format json\|text\|summary]` | Runs all four checks on the working tree, and on its diff from base when one is given. Its notes say when it compared nothing: no base, or a base that is the current commit with nothing changed | Report JSON on stdout, the text queue, or a summary of the verdict, notes, violations and breaking changes with counts of the rest; non-zero exit on failure |
| `treaty overview`, `find`, `impact`, `allowed`, `source` | The MCP tools of the same names, on the working tree | Text on stdout |
| `treaty map [--base <ref>] [--design <name>...]` | Builds, scores and renders the diff, with any designs overlaid | Self-contained HTML file in `.treaty/out/` |
| `treaty design new <name> [--from <target>...]` | Scaffolds a design from the current contracts of the given modules or symbols | `.treaty/designs/<name>.pen`; refuses to overwrite |
| `treaty design check <name>` | Compares a design in `.treaty/designs/` with the current code | Missing, differing, layer-breaking, unclassified and failing items, non-zero exit on failure |
| `treaty slice <target> [--format text\|json]` | Builds a context slice from the current tree. A file slice lists every declaration in the file and the symbols in other files it uses or is used by; a module slice lists its files; every entry has file:line | Text on stdout, one line per entry, or slice JSON |
| `treaty dump [--at <ref>]` | Prints the graph in AutoPen, for debugging and golden tests | AutoPen on stdout |
| `treaty serve [--port <n>] [--open]` | Keeps this directory's live map open until interrupted, starting the treaty server when none runs | The map at `http://127.0.0.1:7878/?p=<folder>` |
| `treaty mcp [--port <n>]` | Relays an agent session over MCP on stdio to the treaty server, starting it when none runs and rejoining it after a restart | MCP on stdio; the map URL on stderr |
| `treaty restart [--port <n>]` | Stops the treaty server; open sessions start a new one and rejoin | A note on stderr |
| `treaty url` | Prints the live map's address for this directory, or fails when its map is not open. In Claude Code, `! treaty url` shows it in the session | The URL |
| `treaty here [--force]` | Registers treaty as an MCP server of the repository, so Claude Code sessions started in it run `treaty mcp`. Creates `.mcp.json`, or adds to an existing one keeping every other entry, the key order and the indentation. A different `treaty` entry is left alone unless `--force` is given | `.mcp.json`, and the next steps |
| `treaty init [--architecture <name>]` | Proposes a config for the named architecture (`none` by default, which declares no architecture) from the shape of the import graph for a person to edit, and creates `.treaty/`. For slices and modular, see Architectures. For hexagonal, clean and layered: programs (such as Go `main` packages) and modules nothing imports are composition; a module that imports nothing in the repository but is used goes innermost, or lowest; every other module sits one layer inside the deepest module importing it. So the proposal has no violations. A library module with no imports either way is left unclassified and listed in the draft. Directory names are hints: they replace the shape's answer only when that adds no violation, and they set an adapter's side, which otherwise comes from whether it implements a core interface (driven) or calls the application (driving) | `treaty.yaml` draft |

Config file, `treaty.yaml`, for the hexagonal architecture (see Architectures for the others):

```yaml
layers:
  domain:      ["internal/graph/**", "internal/lifetime/**"]
  application: ["injection", "injection/scope/**", "injection/ports/**", "injection/errs/**"]
  adapter:
    driving:   ["adapters/codegen/**", "adapters/testkit/**"]
    driven:    ["adapters/reflectx/**", "adapters/slogx/**"]
rules:
  fail_on: [breaking, layer_violation]
  warn_on: [unclassified]
```

Working directory, never tracked:

```
.treaty/
  .gitignore   # contains "*"; written by treaty init
  designs/     # design files
  out/         # rendered maps and reports
```

Because `.treaty/.gitignore` ignores the directory itself, the repository's own `.gitignore` never changes.

## Delivery plan

Three phases, ordered so that each one is useful on its own: layer checks and slices first, because they help agents immediately and need only the graph. The map comes last because it consumes everything else.

&#91;embedded content: delivery plan · 3 phases, 3 gates\]

No dates are set; a phase starts only after the previous gate passes. Build a fixture repository that reproduces the mockup's `smarty/injection` PR #142 in phase 1 and use it as the regression test for every later phase. It lives in `internal/adapters/language/golang/testdata`: `injection-base` is the tree before the PR and `injection` the tree after it, and the tests commit the first and diff the second against it.

## Acceptance criteria

Each criterion is checked by an automated test against the fixture repository unless it says otherwise.

**Architectures**

- [x] On the tool's own repository, the generic engine gives the same graph and check output as the hexagonal-only engine it replaced.
- [x] Each architecture's config loads; an unknown architecture, an unknown clean layer, `slices:` for modular and `contexts:` for slices each fail with an error.
- [x] Slices: an import from one slice into another is a violation; shared code importing a slice is a violation; inside a slice, a lower layer importing a higher one is a violation.
- [x] Modular: an import of another context's internal package is a violation, and each edge of a cycle between contexts is a cycle finding that fails the check.
- [x] `treaty init --architecture` proposes a config for every architecture; for hexagonal, clean and layered the proposal has no violations.
- [x] The layered, slices and modular layouts place every module inside its band, cell or island with no overlaps.

**Phase 1: Graph and layers**

- [x] `treaty check` on the fixture reports exactly one violation, `graph` to `reflectx`, with the reference `graph.typeName` to `reflectx.TypeName` and its file and line.
- [x] `treaty check` passes on the tool's own repository.
- [x] `treaty slice go:injection:Container.Resolve` returns the neighbors and exclusion counts shown in the slice example.
- [x] A module that matches no layer glob is reported as Unclassified, with a warning and exit code 0.

* [x] `treaty dump` on the fixture and on the tool's own repository, parsed back, yields an identical graph.
* [x] Two `treaty dump` runs on the fixture produce identical bytes.

**Phase 2: Contract diff**

- [x] The fixture diff classifies `Container` and `Container.Resolve` as breaking, `Lifetime` and `Binding` as compatible, and exactly four symbols as implementation only.
- [x] Instability for `graph` is reported as 0.33 before and 0.40 after, matching the mockup.
- [x] The review queue order matches a checked-in golden file.

**Phase 3: Map and agents**

- [ ] The generated HTML matches the mockup's panels, encodings and interactions for the fixture. Not yet compared by eye.
- [x] A synthetic graph of 50 modules and 2,000 contract symbols renders in under 2 seconds on a 2024 laptop with no overlapping module hexagons. The layout and HTML are tested; drawing it takes about half a second even in jsdom, checked by hand.
- [x] Every node, module and queue item is reachable and selectable by keyboard. Checked by hand in jsdom on the fixture and the synthetic graph: every module, symbol, file cell, group and queue item takes focus and selects on Enter.
- [x] The MCP server returns the same slice JSON as `treaty slice` for the same symbol.

* [x] `treaty design new probe --from go:injection` followed by `treaty design check probe` reports no findings, and a second `treaty design new probe` fails without touching the file.
* [x] Clicking a symbol in the map shows its code from the source embedded in the HTML file.

## Risks and open questions

The biggest risk and the biggest open question are the same: code the static graph cannot see. Raise each open question in the plan rather than resolving it silently.

**Risks**

| Risk | Effect | Mitigation |
| --- | --- | --- |
| Dependency injection, reflection and interface dispatch hide edges | Missing edges understate blast radius and hide violations | Treat `implements` edges as references; flag reflection call sites as unknown |
| Existing codebases follow no supported architecture cleanly | Most modules land in Unclassified and the map is noise | Five architectures to choose from; `treaty init` proposes a config for the chosen one from the import graph; Unclassified is a warning, not a failure |
| The live map re-lays out as modules appear | The person loses their place mid-session | Anchor the view to the current selection, and keep the center and zoom when nothing is selected |
| Layout degrades past about 12 modules per ring | Map becomes unreadable | Computed placement and edge bundling in phase 3, tested against the 50-module synthetic graph |
| The scanner sees syntax only | Method calls on values of unknown type (`c.g.Order()`) and implicit interface satisfaction must be inferred, so some edges are missed | Resolve names through each file's imports; resolve a method call only when exactly one method of that name is reachable; match method sets structurally for `implements` edges; never guess between candidates |
| A hand-written scanner misreads unusual syntax | Wrong signatures or missing symbols | Tests against deliberately awkward source (generics, raw strings, grouped declarations, comments); the dump round-trip test on the tool's own repository |

**Open questions**

- [x] Should version 1 add runtime traces from the test suite to recover edges hidden by dependency injection? No: edges hidden by dependency injection are out of scope.
- [ ] Is a module a package, or can one module span several packages through config?
- [x] Should breaking changes in internal packages count as breaking, given they cannot be imported from outside? Yes: they are marked absorbed when every dependent changed with them, rank medium, and still fail the check.
- [x] What is the diagram language's final name? AutoPen, formerly CML. Design files end in `.pen`; files saved as `.cml`, and documents that open with `cml 1`, still load.
- [x] Should the live server warn the agent unprompted when an edit introduces a violation? Yes: see Nudging.
- [x] Is polling the tree twice a second fast enough on large repositories? The rate adapts to size instead: twice a second for small and medium repositories, every five seconds for large ones (see Watching).
