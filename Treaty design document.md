# Treaty: design document

Sep 28, 2026 · @Timothy Eckstein

> **Renamed Sep 29, 2026.** The tool was called Contract Map, with the binary `cmap`. It is now Treaty: the binary is `treaty`, the config file is `treaty.yaml` and the workspace is `.treaty/`.

## Summary

Treaty is a tool, written in Go, that reads a codebase into one graph, held in memory in the model of a small diagram language, CML. Everything else is drawn from that graph: a hexagonal map for reviewers, mechanical scores, scoped context slices for coding agents, and design files, scaffolded from existing code, where people sketch new modules and contracts before the code exists. Every score must come from static analysis or test execution, never from a model's judgment.

The tool is ephemeral. It exists for code review, for thinking through a design and for working alongside a coding agent, not for communicating design between team members. It builds the graph from the tree, scores it and serves what was asked for, keeping everything in memory. A one-shot command discards it on exit, and the live server discards it when it stops. Nothing it writes is ever tracked in git.

It is used in two ways. In code review, it shows what a pull request changes in the architecture. While working with an agent, it runs as a live server: the person watches the map update in a browser as code is edited, and the agent uses the same graph through MCP tools to decide where to work without reading the whole codebase. See Live map and MCP server.

The graph's nodes are contract symbols (exported functions, methods, interfaces, types, values) grouped into modules, which are placed into hexagonal architecture layers by a config file. Its scores answer four questions about a change: did a contract change, did a dependency point outward, did stability shift, and do tests that enter through each contract actually kill mutants.

**For Claude Code in planning mode.** Treat this document as the spec, and the HTML mockup of the imaginary repo `smarty/injection` PR #142 as the reference for the visual layer. Plan against the phases in the delivery plan, in order; each phase ends with acceptance checks that can be run. Where this document leaves a choice open, it says so in Risks and open questions; raise those in the plan rather than deciding silently. Keep the tool's own code hexagonal, since it should pass its own checks.

## Problem, goals, non-goals

Agents now produce diffs faster than humans can review them, and line-based review tools rank a 600-line refactor the same as a one-line breaking signature change. Line coverage can read 100% while behavior at the contract goes untested. Agents also sprawl: without an architectural map they read and edit far more of the codebase than a task needs.

**Goals**

1. Rank every change in a diff by contract impact, so reviewers see breaking changes and layer violations first.
2. Score test strength per contract symbol with contract-scoped mutation testing, mechanically and reproducibly.
3. Enforce hexagonal layer rules from a checked-in config, failing the check on outward dependencies.
4. Render an interactive map that stays legible at 50 modules and 2,000 contract symbols through aggregation and semantic zoom.
5. Export a context slice per symbol or module that gives an agent the contracts it needs and nothing else.
6. Let people design new features in CML, then check the design against the code as it is built.
7. Support languages through one extractor interface, each with a small hand-written contract scanner and no language treated as a special case. Version 1 supports Go only.

**Non-goals**

- No AI-generated scores, summaries or risk labels anywhere in the scoring path.
- No tracked outputs. The graph, scores, maps and design files are never committed, and the tool is not a way to hand designs between people.
- Not a UML or C4 replacement and no free-form canvas; every diagram is CML, generated from code or written as a design that is checked against code.
- No runtime tracing in version 1; static analysis plus test execution only (see open questions).
- Go only in version 1. Other languages follow later, each through its own contract scanner behind the same extractor interface.

## Core concepts

Four terms carry the whole design: layer, contract symbol, change kind and test strength. Each is defined so a program can compute it.

**Layers.** Every module belongs to exactly one layer, assigned by path globs in the config. A dependency may point inward or sideways, never outward.

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
| Added | + | New symbol | Medium, higher if weakly tested |
| Implementation only | \~ | Body changed, signature identical | Low |
| Removed | − | Symbol deleted | Treated as breaking if it was exported |
| Moved | → | An exported symbol removed from one place and added in another, as one change | Treated as breaking |
| Unchanged | none | No change | Not listed |

**Test strength.** For a contract symbol, the share of mutants in code reachable from it that are killed by tests which enter through that symbol or another contract. A mutant killed only by a test that calls an internal function directly does not count.

## System architecture

The tool is itself hexagonal: a pure Go core that never calls a parser, git or a test runner directly, with every side effect behind a port. That keeps the core testable with fakes and lets the tool pass its own layer check, with Go's `internal/` packages enforcing some of the boundaries.

Treaty doesn't need a full parser, only contracts: package clauses, imports, top-level declarations and their signatures, struct fields and interface method sets. Bodies only need scanning for the identifiers that become reference edges. So each language gets a small hand-written contract scanner instead of a general-purpose grammar:

- **A lexer** that knows the language's tokens, comments and string forms. For Go it also inserts semicolons at line ends, so declaration boundaries fall out of the token stream.
- **A declaration reader** that walks top-level declarations and renders signatures in CML form. It skips bodies as balanced brackets and never parses an expression or statement.
- **A reference scan** over each declaration's tokens that resolves `pkg.Name` through imports, `recv.Name` through the receiver's type, and bare identifiers within the module.

The same scanner reads CML declaration lines, so design signatures and code signatures are compared by one piece of code. The tool is pure Go with no cgo: it cross-compiles, and `go install` needs no C toolchain. On this repository, the Go scanner produced the same modules, symbols, signatures and fields as a tree-sitter-based extractor it replaced, in about a twentieth of the time.

&#91;embedded content: tool architecture · core, ports and adapters\]

A run moves through these steps in order:

1. The VersionControl adapter materializes the base and head trees.
2. The SourceExtractor builds a graph for each tree: modules, symbols, signatures and reference edges.
3. The config assigns each module a layer; modules that match no glob become Unclassified.
4. The domain classifies each symbol's change kind by comparing base and head signatures.
5. The domain checks layer rules and computes stability metrics on both graphs.
6. The MutationRunner scores test strength for symbols in the blast radius: changed symbols plus every contract that reaches them.
7. The application ranks the review queue and writes the map, report and slices through the Output port.

## Graph data model

One graph per tree, with three node kinds and two edge kinds; everything else is derived. The graph lives only in memory and is never written to a file. The renderer, the check report and the agent slices all read the same in-memory graph in the run that built it.

| Entity | Key fields | Notes |
| --- | --- | --- |
| Module | `id`, `path`, `name`, `layer`, `side`, `files[]` | `name` is what the language calls it, such as the Go package name; `side` is `driving` or `driven` for adapters, empty otherwise |
| Symbol | `id` (module-qualified name), `module`, `file`, `line`, `kind`, `contract`, `signature`, `variants[]` | `kind` is function, method, interface, type or value; each variant is another declaration's file, line, signature and fields |
| File | `path`, `module`, `symbols[]` | Only needed for the inspector and diff attribution |
| Reference edge | `from`, `to`, `kind`, `file`, `line` | `kind` is call, type-use, implements or embeds |
| Containment edge | `module`, `symbol` | Implicit in `Symbol.module`; stored only in the rendered form |

A diff result, also held in memory, pairs two graphs and adds, per symbol, `change` (a change kind), `before_signature`, and `strength` with its `surviving_mutants[]`. Per module it adds before and after values for afferent coupling, efferent coupling, instability, abstractness and distance. Per module edge it adds `new`, `violation` and the underlying references.

Symbol ids must be stable across renames of unrelated code, since agent slices and design files name symbols by id. Use `<language>:<module path>:<qualified name>`, such as `go:injection:Container.Resolve`, `go:internal/graph:Graph.Order` or `c:include/injection.h:inj_resolve`, and never a line number. The module path, not the package name, keeps ids unique when two packages share a name. Each module also records its `language`.

Each function also carries a hash of its normalized body. It is not part of any id or reference; it only keys the mutation-result cache.

## Diagram language (CML)

CML is the language of the graph. For code it exists only in memory: the extractors build it, the scoring reads it, the renderer draws it, and the run ends without writing it anywhere. It is structure only. Change kinds, strength, survivors, metrics and findings are computed on demand and kept in memory beside the graph, never inside it.

The only CML text on disk is design files. A person writes one to sketch a feature before the code exists, and the tool checks it against the code as it is built. So the text syntax is shaped for writing by hand, not for generated output or git diffs. Rough edges are expected and will be fixed as the language gets used.

**What CML must express.** Modules with path and language; contract symbols with kind and signature; dependencies between modules and symbols; design elements that have no code yet; expectations, such as a minimum test strength; and forbidden dependencies. Layers and adapter sides are not written in CML. They always come from `treaty.yaml`.

**Decisions.**

| Decision | Choice | Why |
| --- | --- | --- |
| Base syntax | Paths as headers, each language's own declarations beneath, scoped by indentation | Code reads the way it is written, and nesting shows ownership. A small hand-written parser needs no dependencies. |
| Signature notation | Each language's own syntax, without bodies or receivers | A neutral notation loses information, and breaking-change rules are per language anyway. Receivers are detail below the level of a contract map. |
| Design and generated content | Only design files exist on disk | Generated CML never leaves memory. A design names existing code by symbol id, resolved against the freshly built graph on every run. |
| References | Symbol ids only | Every graph is built from the tree it describes, so nothing can go stale. |
| Expectations | A small fixed set of `@` directives | Every check is already a short, closed list. An expression language would add an evaluator to the core. |

**Shape.**

- The first line is `cml 1`. A design's second line is `design "<title>"`.
- Scope comes from indentation: exactly two spaces per level, and tabs are an error.
- A line at column zero is a header: `<lang>:<path>`. A path ending in a source file extension names a file, such as `go:adapters/database/DB.go`. Any other path names a module, such as `go:injection`, for designs where no file has been chosen yet. A file's module is derived from it: for Go, its directory.
- An indented line is a declaration in the header's language, one per line, without a body. Nesting shows ownership: methods and fields sit under their type. In Go, a nested line starting with `func` is a method and any other nested line is a field.
- A line starting with `@` is a directive, not code. No supported language starts a declaration with `@`. TypeScript decorators are not part of a signature, so they never appear.
- `//` at the start of a line is a comment.
- CML's parser reads only headers, indentation and directives. The text of each declaration goes to that language's snippet parser.

Whether a symbol is part of the contract comes from the native syntax: in Go, an exported name, and for a method, an exported method on an exported type. Symbol ids are unchanged, `<lang>:<module path>:<qualified name>`, so the method below is `go:adapters/database:MySQL.Create` whichever file it lives in.

**Directives.**

| Directive | Placed under | Meaning |
| --- | --- | --- |
| `@expect strength >= N` | header, declaration | Contract-scoped test strength |
| `@expect instability <= N` or `>= N` | header | Martin's I |
| `@expect abstractness >= N` or `<= N` | header | Martin's A |
| `@expect distance <= N` | header | Martin's D |
| `@depends <target>` | header, declaration | Intended dependency; checked against layer rules immediately, before any code exists |
| `@forbid <target>` | header, declaration | No dependency on the target; globs allowed |

**Examples.** A design, `.treaty/designs/scoped-lifetimes.cml`:

```
cml 1
design "Scoped lifetimes"

go:injection
  type Container interface
    func Resolve(ctx context.Context, k Key) (any, error)
    func Scope() scope.Scope
  const Scoped Lifetime = 2

go:injection/scope/scope.go
  @forbid go:injection/ports
  type Scope interface
    func Resolve(ctx context.Context, k injection.Key) (any, error)
    func Dispose() error
      @expect strength >= 0.80
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

**Designing in CML.** A design is partial. It lists only what it adds or pins down, and code it doesn't mention is ignored. Whether an element is new isn't written down; the tool works it out against the current graph. A file in a header is a hint: symbols are matched by id, so moving code between files never counts as drift.

**Starting a design.** `treaty design new <name> [--from <module or symbol>...]` writes `.treaty/designs/<name>.cml`. It contains the header, then for each `--from` target the file headers and contract declarations from today's code. Internal symbols, locations and edges are left out. With no `--from`, the file has only the header. The command refuses to overwrite an existing design.

The person then edits declarations, adds headers and declarations, and adds directives. Scaffolded lines left unedited stay in the design and pin those signatures: `design check` reports if the code drifts from them. Deleting a line drops that pin. The map is display-only for designs; it draws them with `treaty map --design` but never edits them.

**Signature comparison.** Each language's scanner also reads CML declaration lines, using the same code that reads source, so a design signature and a built signature go through one parser. Signatures are compared by structure, ignoring parameter names: `func Dispose(ctx context.Context) error` matches `func Dispose(c context.Context) (err error)`. Receivers are never written. The graph still records whether a Go method has a pointer or value receiver, the contract diff still reports a change as breaking, and the inspector shows it.

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
- `@variant <file>:<line>` under a symbol for each build variant.

Design files may not use these. Output is canonical: headers are sorted by path, declarations follow source order, and edges are sorted by kind, target and location. It exists for debugging and golden tests. Parsing it back must yield the same graph, which proves that the snippet parsers and the extractors share one model.

```
go:adapters/reflectx/name.go
  func TypeName(t reflect.Type) string @7

go:internal/graph/names.go
  func typeName(t reflect.Type) string @9
    @call go:adapters/reflectx:TypeName @11
```

## Mechanical scoring

Five checks run on every diff, and each one is reproducible from the two trees, the config and the test suite alone. Same inputs, same numbers.

**1. Contract diff.** Compare base and head signatures from each language's extractor, not text. Each language supplies its own breaking-change rules. Go follows the rules of `apidiff`: removed exports, changed parameters or results, and new methods on exported interfaces are breaking; renamed parameters, added struct fields and changed constant values are compatible.

A removed exported symbol pairs with an added one, and becomes a single moved change, when exactly one candidate matches on either side: the same qualified name in a different module, or the same module and signature under a different name. The methods and fields of a moved type fold into the type's change when their signatures are unchanged. When a type's fields change, the change lists the field lines removed and added.

A symbol declared more than once in a module, in files built under different constraints such as Go build tags, is one symbol with variants. It keeps the first declaration's location and signature, its reference edges are the union of every variant's, each carrying its own file, and its body hash covers every variant, so a change to one platform's code alone still shows. Variants whose signatures or fields disagree are reported as a finding.

**2. Layer rules.** A module edge is a violation when the target's layer rank is higher than the source's, or when the target's layer is not in the source's allowed list. Composition may depend on anything, and any edge into composition from inside the hexagon is a violation. Violations fail the check by default. Unclassified modules produce a warning, not a failure.

**3. Stability metrics.** Robert Martin's package metrics, computed at module level on both graphs, with afferent coupling Ca and efferent coupling Ce counted as distinct modules:

```latex
I = \frac{C_e}{C_a + C_e} \qquad A = \frac{\text{exported interfaces}}{\text{contract symbols}} \qquad D = \lvert A + I - 1 \rvert
```

The report flags a domain module whose instability rises, and an adapter that other modules depend on.

**4. Contract-scoped test strength.** For each contract symbol in the blast radius, generate mutants in every function reachable from it within its module. Run only the tests whose call stacks enter through a contract symbol, found by per-test coverage. Strength is killed mutants over total mutants, excluding mutants proven equivalent. Report each survivor with file, line, the original and mutated expression, and the operator used.

**5. Review ranking.** Each finding gets a severity from its kind, and the queue sorts by severity, then by blast radius (count of contracts that transitively depend on the symbol).

| Finding | Severity |
| --- | --- |
| Breaking contract change | High |
| Layer violation | High |
| New or changed contract with strength below the threshold (default 60%) | High |
| Interface change that breaks implementers | Medium |
| New module or new contract symbols | Medium |
| Compatible contract change | Low |
| Implementation-only changes, grouped | Low |

Thresholds live in the config. The first release ships the defaults above and reports rather than fails on test strength, so teams can calibrate before gating on it.

## Visual design

The HTML mockup is the reference: three panels, with the review queue on the left, the hexagon map in the middle and the inspector on the right. The same page runs two ways. Served live, it loads the view from the server and redraws on every rebuild (see Live map and MCP server). Written by `treaty map`, it is a single self-contained HTML file with the view embedded and no server. Either way the view embeds the source span of each symbol it shows, so the inspector can display code without reaching the repository.

**Map.** Three concentric flat-top hexagons: domain in the center, application in the middle ring, adapters outside. Driving adapters sit on the left and driven adapters on the right, with composition at the far left of the adapter ring. A module is labelled with its path; the repository root, whose path is `.`, is labelled with the name its language gives it, such as its Go package name. Each module is a small hexagon inside its ring. Its contract symbols sit evenly spaced on the module's border, and internal symbols appear inside only when the "Show internals" toggle is on or a selection touches them.

**Encoding.** Nothing relies on color alone; every color has a paired shape or glyph.

| Channel | Encodes | Values |
| --- | --- | --- |
| Node shape and fill | Symbol kind | Hollow circle function, filled circle method, hollow square interface, filled square concrete type, filled triangle value |
| Color and glyph | Change kind | Grey unchanged; blue with + added; orange with Δ compatible, ! breaking, → moved or \~ implementation only; teal and dashed for unimplemented. Hollow shapes take the color on their outline, filled shapes in their fill; the glyph sits beside the icon |
| Ring around node | Test strength | Arc length equals the share of mutants killed |
| Module border | New module; composition | Dashed blue; a double outline for composition |
| Teal and dashed | Unimplemented: designed or planned, not yet built | Teal (`#0d9488` light, `#2dd4bf` dark) on the symbol or module outline, always with a dashed outline so the state doesn't rely on color. It turns blue with + once built |
| Module edge | Dependency | Width grows with reference count; blue when new |
| Dashed edge with hollow arrowhead | Implements or embeds | Drawn separately from solid "uses" edges; a module pair with both bows the two curves apart |
| Magenta edge with ! marker | Layer violation | Always drawn, even with module edges hidden |

**Aggregation.** By default, edges are drawn between modules only. Selecting a symbol draws its symbol-level edges and labels, and dims everything unrelated. The mockup's hand-placed layout does not scale: at more than about 12 modules per ring, module positions and edge routing must be computed, with edges bundled along the gaps between rings.

**Groups.** Within each ring, a directory with two or more packages or sub-directories becomes a group: a hexagon drawn around them, nested as deep as the directories go. A directory with a single child is skipped, a package with sub-packages is a group whose center is the package itself, and a group that would hold a ring's entire contents is unwrapped onto the ring. Groups are visual only: layer rules, metrics and slices stay per package. The layout is computed bottom-up for the fully expanded tree, so collapsing a group never moves anything. A group collapses on its own when it is small on screen, showing its name and counts, and edges attach to it instead of the packages inside; double-clicking or selecting something inside it expands it.

**Review queue.** Findings sorted as described under Mechanical scoring. Each item shows a severity icon, a title and one sentence, and selecting it selects the related symbol, module or edge on the map.

**Inspector.** Its content depends on the selection:

- **Symbol:** qualified name, kind, file, change kind, a line diff of the signature, the strength meter with surviving mutants, and clickable dependency and caller chips.
- **Module:** files with change status, the five stability metrics before and after, the guidance for its layer, and its contract chips.
- **Module edge:** the rule, whether it passes, and each underlying reference with file and line.

Every inspector view ends with the agent context slice for that selection and a copy button. The page supports light and dark themes, keyboard selection of every node, and respects reduced-motion settings.

## Live map and MCP server

*Decided and built Sep 29, 2026.*

The map stops being a static page that has to be regenerated. A long-running server holds the graph in memory, watches the working tree, re-extracts it when files change, and pushes each update to every open browser with server-sent events. Each event carries the new version, baseline and any build error, and the page then fetches the view, which avoids a second protocol and needs nothing beyond the standard library. The server listens on 127.0.0.1 only, on port 7878 or any free port if that is taken; it refuses requests addressed to any other host name, which blocks DNS rebinding, and it accepts writes only as JSON, which a web page on another origin cannot send without a preflight the server never grants. The same process serves the MCP tools, so the person in the browser and the agent in Claude Code always look at the same graph.

**Two ways to start it.**

- **Pinned to a session.** Claude Code starts `treaty mcp` over stdio as an MCP server. It lives as long as the session, and it serves the map on a local port for the person to open; the MCP instructions give the agent the URL.
- **On its own.** A person runs `treaty serve [--open]`, to review a pull request or explore a codebase with no agent involved.
- **Both.** A running server records its URL in `.treaty/server.json`. A session started later finds it, serves no second map, and follows that server's baseline and selection, so the person keeps one browser tab. A second `treaty serve` in the same directory also points to the running map instead of starting another.

**Finding the map.** Claude Code does not show an MCP server's output to the person, only to its logs, so the link reaches them through the agent: the server's MCP instructions give the agent the URL, say whether the map is new or the one already running, and ask it to share the link in its first reply. The `overview` result starts with the link too, and `treaty url` prints it on demand.

Either way it writes nothing tracked, and its state disappears when it stops. The static `treaty map` output stays available for a one-off snapshot.

**Baseline.** Changes are always shown against a baseline, and the person can change the baseline from the map's header while the server runs, without restarting it. There are three modes:

- **HEAD**, the default, which shows uncommitted work.
- **Pull request**, for code review: the merge base of HEAD and the target branch, so that every commit on the branch counts, however many there are. The target defaults to the remote's default branch (`origin/HEAD`, else `origin/main`, `origin/master`, `main` or `master`), and the person can type another.
- **Ref**, any revision, such as `HEAD~3`.

The server re-resolves the baseline every two seconds, so a commit moves a HEAD baseline forward. Each baseline tree is extracted once and cached by commit.

**Watching.** The server fingerprints the path, size and modification time of every file outside hidden, vendor and dependency directories, plus `treaty.yaml` and the designs, twice a second, and rebuilds when the fingerprint changes. A failed build, such as a config that does not parse mid-edit, keeps the last good map and shows the error in the header.

**Selection and pointing.** The agent reads what the person has selected on the map with `selection`, so the person can click a module and say "work here." The agent can also ask the person to look at something with `show`, but it never takes the view from them:

- **An offer, not a move.** By default `show` does not move the view or change the selection. The item pulses in teal, and a notice in the corner of the map gives the agent's reason with *Go* and *Dismiss*. The view moves only when the person clicks *Go*. An unanswered notice fades after 20 seconds, and a new one replaces it rather than stacking.
- **Follow Claude.** A switch in the header, off by default and remembered per browser, lets `show` move the view, but only after the person has left the map alone for 4 seconds. While they are using it, a request is an offer as usual.
- **Rate limit.** The server accepts one `show` every 15 seconds. A request sooner is refused with how long to wait, so the agent learns to hold back; a session that follows another server forwards its requests there, so the limit covers every session.
- **Guidance.** The tool's description tells the agent to use it only when it needs the person's eyes on one specific thing, such as before asking about it or when something needs a decision, and never to narrate progress.

**Layout under change.** Modules may move as others are added or removed; positions are not frozen to a baseline. The view is anchored to the current selection instead. If a selected module sits in the lower left of the screen and a re-layout moves it, the view pans and zooms with it, so it stays where the person left it. With nothing selected, the view keeps its center and zoom.

**MCP tools.** Every tool answers from the in-memory graph, in compact CML where possible, so an agent can orient itself cheaply:

| Tool | Answers |
| --- | --- |
| `overview` | The whole architecture in a few kilobytes: modules by layer, contract counts, module edges. The agent's first call, instead of exploring files |
| `find` | Symbols by name or kind, with id, file, line and signature |
| `slice` | The context for working on one symbol or module (exists) |
| `impact` | What depends on a symbol, transitively: its blast radius, before an edit |
| `allowed` | Whether one module may depend on another, before an import is added |
| `changes` | What changed in the architecture since the baseline: contract changes, new and removed edges, new violations. The agent checks its own edits with this |
| `plan` | Writes unimplemented CML: the contracts and dependencies the agent intends to build, in design syntax. It is validated like a design, so it reports layer problems and conflicts with existing code, and it is overlaid on the live map as unimplemented. As the code is written, each item turns into a built one, so the person can watch the plan being filled in. A plan is saved as a design file in `.treaty/designs/`, so it survives a restart, `design check` works on it, and the person can open and edit it |
| `selection` | What the person has selected on the map |
| `show` | Asks the person to look at one symbol or module: it pulses and a notice offers to jump there. At most once every 15 seconds |
| `check`, `design_check` | As today (exist) |

**Nudging.** For now the agent learns about violations only by calling `changes`. Whether the server should tell the agent unprompted, for example by attaching a warning to its next tool result, is an open question to answer after using the tool for a while.

## Agent context slice

A slice is the minimum an agent needs to work on one symbol or module without reading the rest of the codebase: the target's contract, its neighbors' contracts, and the layer rules it must not break. Implementations outside the target module are never included.

Example slice for `go:injection:Container.Resolve`, abbreviated:

```json
{
  "schema": "treaty/slice/v1",
  "task_scope": {
    "symbol": "go:injection:Container.Resolve",
    "file": "injection/resolve.go",
    "layer": "application",
    "kind": "method"
  },
  "contract": {
    "signature": "Resolve(ctx context.Context, k Key) (any, error)",
    "change": "breaking"
  },
  "may_depend_on_layers": ["domain", "application"],
  "neighbors": [
    {"symbol": "go:injection:resolveLocked", "relation": "uses", "signature": "..."},
    {"symbol": "go:internal/graph:Graph.Order", "relation": "uses", "signature": "func (g *Graph) Order() ([]Binding, error)"}
  ],
  "tests": {"strength": 0.86, "surviving_mutants": []},
  "excluded": {"modules": 7, "symbols": 21}
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

The tool ships as a single binary, `treaty`, driven by a config file at the repository root. The one-shot commands below build the graph fresh on each call. The live server, which also serves MCP, is described in Live map and MCP server; `treaty serve` and `treaty mcp` start it. Everything the tool writes goes under `.treaty/` at the repository root, which ignores itself.

| Command | Does | Output |
| --- | --- | --- |
| `treaty check --base <ref>` | Runs all five checks on the diff from base to HEAD | Report JSON on stdout, non-zero exit on failure |
| `treaty map [--base <ref>] [--design <name>...]` | Builds, scores and renders the diff, with any designs overlaid | Self-contained HTML file in `.treaty/out/` |
| `treaty design new <name> [--from <target>...]` | Scaffolds a design from the current contracts of the given modules or symbols | `.treaty/designs/<name>.cml`; refuses to overwrite |
| `treaty design check <name>` | Compares a design in `.treaty/designs/` with the current code | Missing, differing, layer-breaking, unclassified and failing items, non-zero exit on failure |
| `treaty slice <symbol or module>` | Builds a context slice from the current tree | Slice JSON on stdout |
| `treaty dump [--at <ref>]` | Prints the graph in CML, for debugging and golden tests | CML on stdout |
| `treaty serve [--port <n>] [--open]` | Serves the live map until interrupted | The map at `http://127.0.0.1:7878` |
| `treaty mcp [--port <n>]` | Serves the live graph to an agent over MCP on stdio, and the live map to the person unless another server already does | MCP on stdio; the map URL on stderr |
| `treaty url` | Prints the live map's address for this directory, or fails when no treaty server is running here. In Claude Code, `! treaty url` shows it in the session | The URL |
| `treaty here [--force]` | Registers treaty as an MCP server of the repository, so Claude Code sessions started in it run `treaty mcp`. Creates `.mcp.json`, or adds to an existing one keeping every other entry, the key order and the indentation. A different `treaty` entry is left alone unless `--force` is given | `.mcp.json`, and the next steps |
| `treaty init` | Proposes a layer config from the shape of the import graph for a person to edit, and creates `.treaty/`. Programs (such as Go `main` packages) and modules nothing imports are composition; a module that imports nothing in the repository but is used is domain; every other module sits one ring inside the deepest module importing it. So the proposal has no violations. A library module with no imports either way is left unclassified and listed in the draft. Directory names are hints: they replace the shape's answer only when that adds no violation, and they set an adapter's side, which otherwise comes from whether it implements a core interface (driven) or calls the application (driving) | `treaty.yaml` draft |

Config file, `treaty.yaml`:

```yaml
layers:
  domain:      ["internal/graph/**", "internal/lifetime/**"]
  application: ["injection", "injection/scope/**", "injection/ports/**", "injection/errs/**"]
  adapter:
    driving:   ["adapters/codegen/**", "adapters/testkit/**"]
    driven:    ["adapters/reflectx/**", "adapters/slogx/**"]
rules:
  fail_on: [breaking, layer_violation]
  warn_on: [unclassified, weak_contract]
  strength_threshold: 0.60
mutation:
  scope: blast_radius   # or: all
  timeout_per_mutant: 30s
```

Working directory, never tracked:

```
.treaty/
  .gitignore   # contains "*"; written by treaty init
  designs/     # design files
  cache/       # mutation results keyed by function hash
  out/         # rendered maps and reports
```

Because `.treaty/.gitignore` ignores the directory itself, the repository's own `.gitignore` never changes.

## Delivery plan

Four phases, ordered so that each one is useful on its own: layer checks and slices first, because they help agents immediately and need only the graph. The map comes last because it consumes everything else.

&#91;embedded content: delivery plan · 4 phases, 4 gates\]

No dates are set; a phase starts only after the previous gate passes. Build a fixture repository that reproduces the mockup's `smarty/injection` PR #142 in phase 1 and use it as the regression test for every later phase.

## Acceptance criteria

Each criterion is checked by an automated test against the fixture repository unless it says otherwise.

**Phase 1: Graph and layers**

- [ ] `treaty check` on the fixture reports exactly one violation, `graph` to `reflectx`, with the reference `graph.typeName` to `reflectx.TypeName` and its file and line.
- [ ] `treaty check` passes on the tool's own repository.
- [ ] `treaty slice go:injection:Container.Resolve` returns the neighbors and exclusion counts shown in the slice example.
- [ ] A module that matches no layer glob is reported as Unclassified, with a warning and exit code 0.

* [ ] `treaty dump` on the fixture and on the tool's own repository, parsed back, yields an identical graph.
* [ ] Two `treaty dump` runs on the fixture produce identical bytes.

**Phase 2: Contract diff**

- [ ] The fixture diff classifies `Container` and `Container.Resolve` as breaking, `Lifetime` and `Binding` as compatible, and exactly four symbols as implementation only.
- [ ] Instability for `graph` is reported as 0.33 before and 0.40 after, matching the mockup.
- [ ] The review queue order matches a checked-in golden file.

**Phase 3: Test strength**

- [ ] Two runs on a clean checkout produce identical strength values and survivor lists.
- [ ] `scope.Scope.Dispose` reports the three seeded survivors from the fixture.
- [ ] A mutant killed only by a test that calls an internal function directly is not counted as killed.

**Phase 4: Map and agents**

- [ ] The generated HTML matches the mockup's panels, encodings and interactions for the fixture.
- [ ] A synthetic graph of 50 modules and 2,000 contract symbols renders in under 2 seconds on a 2024 laptop with no overlapping module hexagons.
- [ ] Every node, module and queue item is reachable and selectable by keyboard.
- [ ] The MCP server returns the same slice JSON as `treaty slice` for the same symbol.

* [ ] `treaty design new probe --from go:injection` followed by `treaty design check probe` reports no findings, and a second `treaty design new probe` fails without touching the file.
* [ ] `treaty design check scoped-lifetimes` reports `scope.Scope.Dispose` below its 0.80 expectation and no missing symbols.
* [ ] Clicking a symbol in the map shows its code from the source embedded in the HTML file.

## Risks and open questions

The biggest risk is mutation testing cost; the biggest open question is how to handle code the static graph cannot see. Raise each open question in the plan rather than resolving it silently.

**Risks**

| Risk | Effect | Mitigation |
| --- | --- | --- |
| Mutation runs are slow on large blast radii | Reviewers skip the check or wait on it | Blast-radius scope by default, per-mutant timeout, cache results by function hash |
| Dependency injection, reflection and interface dispatch hide edges | Missing edges understate blast radius and hide violations | Treat `implements` edges as references; flag reflection call sites as unknown |
| Existing codebases are not hexagonal | Most modules land in Unclassified and the map is noise | `treaty init` proposes layers from the import graph; Unclassified is a warning, not a failure |
| The live map re-lays out as modules appear | The person loses their place mid-session | Anchor the view to the current selection, and keep the center and zoom when nothing is selected |
| Layout degrades past about 12 modules per ring | Map becomes unreadable | Computed placement and edge bundling in phase 4, tested against the 50-module synthetic graph |
| The scanner sees syntax only | Method calls on values of unknown type (`c.g.Order()`) and implicit interface satisfaction must be inferred, so some edges are missed | Resolve names through each file's imports; resolve a method call only when exactly one method of that name is reachable; match method sets structurally for `implements` edges; never guess between candidates |
| A hand-written scanner misreads unusual syntax | Wrong signatures or missing symbols | Tests against deliberately awkward source (generics, raw strings, grouped declarations, comments); the dump round-trip test on the tool's own repository |
| Teams game the strength score with shallow tests | Scores rise without better tests | Mutation testing resists this by design; report survivors, not just the ratio |

**Open questions**

- [ ] Should version 1 add runtime traces from the test suite to recover edges hidden by dependency injection, or wait until phase 4?
- [ ] Is a module a package, or can one module span several packages through config?
- [ ] Which mutation tool to wrap per language, or whether to write one in Go on top of the contract scanners.
- [ ] Should breaking changes in internal packages count as breaking, given they cannot be imported from outside?
- [ ] What is CML's final name?
- [ ] Should the live server warn the agent unprompted when an edit introduces a violation, or is the `changes` tool enough? Answer after using the tool for a while.
- [ ] Is polling the tree twice a second fast enough on large repositories, or does the watcher need the operating system's file events?
