# Treaty

Treaty reads a codebase into a graph of its contracts, places them in the
architecture your team declares (hexagonal, clean, layered, vertical slices
or modular monolith), and checks, slices and draws that graph. You can use it
for code review, for thinking through a design, and alongside a coding
agent.

## Why Treaty exists

Coding agents now write diffs faster than people can review them, and our
review tools haven't kept up:

- **Line-based review treats all changes alike.** A diff viewer shows a
  600-line refactor that changes no behavior with the same weight as a
  one-line signature change that breaks every caller. The reviewer has to
  find the change that matters on their own.
- **Coverage shows which lines ran, not what was tested.** Line coverage can
  read 100% while nothing checks the behavior a caller relies on.
- **Architecture is enforced by memory.** Layer rules live in people's heads
  and slip one import at a time. Code review rarely catches a dependency that
  points the wrong way, because it looks like any other import.
- **Agents sprawl.** An agent with no map of the architecture reads and edits
  far more of the codebase than its task needs, and costs more context and
  review effort for it.

Treaty's answer is to treat a codebase's **contracts** as the thing worth
looking at: the exported functions, methods, interfaces, types and values
that modules offer each other. Everything behind a contract is an
implementation detail, and it is judged through the contracts that reach it.

## Principles

- **Contracts first.** Treaty ranks every change by what it does to a
  contract (breaking, compatible, added, removed, moved or implementation
  only), using signature comparison instead of a text diff. So reviewers see
  breaking changes and rule violations first.
- **Architecture is declared and checked.** `treaty.yaml` names the
  architecture and places each module in it. Treaty fails the check when a
  dependency breaks that architecture's rules, and says which rule. Choosing
  the architecture is up to the team; Treaty doesn't recommend one.
- **Every score is mechanical.** Scores come from static analysis, never
  from a model's judgment. There are no AI-generated
  scores, summaries or risk labels anywhere in the scoring path, so every
  result is reproducible.
- **Ephemeral by design.** The graph lives in memory. Treaty doesn't
  communicate a design between people or keep it as a record. Nothing it
  writes is ever tracked in git. Its workspace, `.treaty/`, ignores itself.
- **Give agents only what they need.** A context slice holds the contracts
  around one symbol or module and the architecture rules it must not break, and
  counts what it left out. An agent can work on the right code without
  reading the whole repository.
- **Small, hand-written scanners.** Treaty needs contracts, not a full
  parse tree. Each language gets a small hand-written contract scanner
  behind one extractor interface. The tool is pure Go with no cgo, so
  `go install` just works. Go, JavaScript, TypeScript and Python are supported.
- **Treaty passes its own checks.** The tool is hexagonal itself: a pure core
  with every side effect behind a port.

## Install

Treaty requires Go 1.27 or later.

```sh
go install github.com/smarty/treaty/cmd/treaty@latest
```

## Getting started

In the root of a Go repository:

```sh
treaty init --architecture layered   # propose a treaty.yaml from the import graph
treaty check --base main             # check the rules and contract changes; exits 1 on failure
treaty serve --open
```

`treaty init` fits the code to the architecture you name, using the shape of
the import graph. Directory names only act as hints. Without
`--architecture`, it writes `architecture: none`: no architecture, so every
dependency is allowed while contracts and their changes are still checked.
A repository with no `treaty.yaml` is treated the same way. Review and edit
the draft before you rely on it.

`treaty check` without `--base` checks only the architecture, and says so. Give
it the ref your work started from to classify contract changes too.

A hexagonal `treaty.yaml` looks like this:

```yaml
layers:
  composition: ["cmd/**"]
  domain:      ["internal/graph/**", "internal/rules/**"]
  application: ["internal/app/**"]
  adapter:
    driving:   ["internal/adapters/cli/**", "internal/adapters/web/**"]
    driven:    ["internal/adapters/language/**", "internal/adapters/gitvcs/**"]
rules:
  fail_on: [breaking, layer_violation]
  warn_on: [unclassified]
```

## Architectures

Every architecture shares two rules: composition (usually `main`) may depend
on everything, and nothing may depend on composition.

| Architecture | Rules | Map |
| --- | --- | --- |
| `none` (default) | No architecture: every dependency is allowed. Contracts and their changes are still checked | One region holding every module |
| `hexagonal` | Dependencies point inward: domain ← application ← adapters. No adapter uses another adapter | Rings, with driving adapters on the left and driven on the right |
| `clean` | Dependencies point inward: entities ← use cases ← interface adapters ← frameworks | Four rings |
| `layered` | A layer may use itself and every layer below it. Skipping a layer is allowed; forbidding it is a team decision | Horizontal bands |
| `slices` | A slice may use itself and shared code, never another slice. Optional layers inside every slice | A column per slice, or a grid of slices by layer |
| `modular` | A context may use its own packages, other contexts' public packages and shared code. No cycles between contexts | An island per context |

```yaml
architecture: layered
composition: ["cmd/**"]
layers:                                # top to bottom
  presentation: ["internal/web/**"]
  business:     ["internal/service/**"]
  data:         ["internal/store/**"]
```

```yaml
architecture: slices
composition: ["cmd/**"]
shared:      ["internal/platform/**"]
slices:      ["internal/features/*"]   # each match is one slice
layers:                                # optional, inside every slice, top to bottom
  api:   ["."]
  store: ["store/**"]
```

```yaml
architecture: modular
composition: ["cmd/**"]
shared:      ["internal/platform/**"]
contexts:    ["internal/*"]            # each match is one context
public:      [".", "api/**"]           # what other contexts may use, relative to each context
```

For `layered`, the layer names are yours. For `clean`, use
the layer names `entities`, `use_cases`, `interface_adapters` and
`frameworks`. For `layered`, `hexagonal` and `clean`, a glob that is exactly
a module's path, such as `internal/web/admin`, wins over every wildcard, so
one package can sit in a different layer from its directory's glob. The
design document has the full rules.

Treaty reads the whole repository, including sub-trees with their own
`go.mod`. Their imports resolve through their own module paths, and imports
between them and the rest of the repository count like any other.

## The live map

One treaty server runs on your machine and holds the graph of every
repository you work in. Its one page, `http://127.0.0.1:7878/`, has a tab for
each open repository, so a single browser tab follows them all;
`http://127.0.0.1:7878/?p=<folder>` opens with that repository's tab shown. It
watches each working tree and pushes
every rebuild to the browser. `treaty serve` keeps the current directory's map
open; Claude Code sessions open it too (see below). The map lays modules
out to match the architecture. Each module holds a ring of cells, one per file,
darker for files with more symbols, so crowded files stand out. The module's
contracts stay on its border, each on the stretch facing its file; zoom in and
each file shows its internals as their kind, and pointing at a file or a contract
lights up the other. Changes are shown against a baseline: HEAD for
uncommitted work, the merge base for a pull request, or any ref. You can
change the baseline from the map's header while it runs. The server listens
on `127.0.0.1` only, always on port 7878 unless you pass `--port`.

The server starts with the first session that needs it, and it runs apart from
that session. A repository's map closes 30 seconds after the last session in it
ends, and the server stops 30 seconds after the last map closes. When
`treaty` is rebuilt, the server stops, and the open sessions start the new one
and rejoin it without restarting Claude Code. `treaty restart` does the same
by hand. The server logs to `~/.treaty/daemon.log`.

The header also has an architecture dropdown. Picking another architecture
previews your code in it, fitted the way `treaty init` would propose. Checks
and agents keep following `treaty.yaml` until you press *Use this
architecture*, or until the preview has been left in place for five minutes.
Then `treaty.yaml` is replaced with the proposal.

The review queue and the inspector are tabs in drawers beside the map. Drag
a divider to resize a drawer. Drag a tab to move its panel into another
drawer, split a drawer, join the map as a tab, or float it over the map;
drop it on a tab bar to put it among those tabs, which also reorders them.
The map's views and the project tabs reorder the same way, but don't dock.
*Reset layout* restores the default.
Click a file's cell to select it, just like a module or symbol: the
inspector shows the file's code and its agent context slice, and a symbol
shows its documentation and code. Within a file, symbols run values, types,
interfaces, methods, then functions, each by name: the contracts clockwise
from the left end of the file's bracket on the border, and the internals
across the file's cell. A package that holds a `go.mod` shows it in the first
cell of its ring, at 12:00; click it to read the file.
Hide the key at the top of the map with its *Hide* button or `k`, and bring it
back with *Key* or `k` again. Selecting never moves the map. A selection
hidden in a collapsed group lights that group, and the inspector's *Go to*
takes you there.

To move a module, press and hold it without moving for half a second to
pick it up, then drag it. The band, ring or area under the pointer lights
up, and the tip says what a drop will do. Escape puts the module back.
- **In its own band, ring or area:** the module stays where you drop it. The
  position is saved for this repository and architecture in
  `.treaty/positions.json`. Its group grows to keep it inside.
  *Put it back* in the inspector returns it to its computed place.
- **In another layer** (`layered`, `hexagonal` and `clean`), including a side
  of the adapter ring or composition: Treaty edits `treaty.yaml` to move the
  module there, keeping the file's comments. It adds the module's exact path
  to the new layer's list and removes it from any other. The rules, checks and
  agents follow at once. Dropping an unclassified module on a layer
  classifies it. Moves between layers wait while another architecture is
  previewed.

The Theme menu offers System (following your OS) and 21 themes: Light and
Dark in the style of VS Code's defaults; High Contrast and Color-blind Safe
versions of each for accessibility; and Dawn, Dusk, Fjord, Vampire, Harvest,
Blossom, Great Wave, Gumdrop, Gumdrop Light, Neon Rain, Comic, Midnight, DOS
(dark), DOS (light) and Windows 3.x.
Themes are JSON files in `~/.treaty/themes/`. Treaty rewrites its own there
every time a server starts, so copy one to a new name to make your own; any
other `.json` file there shows up in the menu under Yours.

Your theme, layout and *Follow Claude* choice are saved in
`~/.treaty/settings.json`, so they follow you across repositories, browsers
and sessions.

## Working with Claude Code

```sh
treaty here
```

This registers Treaty in the repository's `.mcp.json`. Each Claude Code
session started in the repository then runs `treaty mcp`. That process relays
the session to the treaty server, starting the server when none is running.
The server serves the repository's live graph to the agent as MCP tools, and
the map to you. A second session in the same repository shares the same map.
You and the agent always look at the same graph:

| Tool | Answers |
| --- | --- |
| `overview` | The whole architecture in a few kilobytes: modules by layer, contract counts, module edges |
| `find` | Symbols and struct fields by name or kind, with id, file, line and signature |
| `slice` | The context for working on one symbol, file or module, one line per entry with file:line |
| `source` | Just the code needed: a symbol from its documentation to its last line, or a file's lines, numbered. A long file gives its outline unless asked for all of it |
| `impact` | Everything that depends on a symbol, file or module, transitively, before an edit |
| `allowed` | Whether one module may depend on another, before an import is added |
| `changes` | What changed in the architecture since the baseline, including new violations |
| `plan` | The agent's intended contracts in AutoPen, drawn on the map as unbuilt and filled in as the code is written |
| `selection` | What the person has selected on the map, so they can point and say "work here" |
| `show` | Asks the person to look at one thing. It is an offer on the map, never a forced move, and limited to once every 15 seconds |
| `check`, `design_check` | The same checks as the CLI; `check` gives the summary by default |

Wherever a tool takes a target, it may be an id, a file or a short name that
matches exactly one thing, such as `Store.CreateBook`, `CreateBook` or
`store.go`. An ambiguous name lists its candidates.

The agent doesn't have to ask about violations. Every tool result ends with a
warning about any layer violation it hasn't been told about yet, such as one
its last edit introduced, and asks it to fix the violation or explain it.

The server watches the working tree twice a second, or every five seconds in
a large repository (20,000 files or more, or one slow to walk).

## Commands

| Command | Does |
| --- | --- |
| `treaty -C <dir> <command>` | Runs any command on the repository at `dir`, as `git -C` does; `--dir <dir>` also works |
| `treaty check [--base <ref>] [--format json\|text\|summary]` | Runs every check on the working tree, and on its diff from base when one is given; exits 1 on failure. `summary` lists only the verdict, notes, violations and breaking changes, and counts the rest |
| `treaty overview [--base <ref>]` | Prints every module by layer and every module dependency, in a few kilobytes |
| `treaty find <query> [--kind <kind>]` | Lists symbols and struct fields whose id contains the query, with file:line and signature |
| `treaty slice <target> [--format text\|json]` | Prints a context slice, one line per entry with file:line; `--format json` for the full form |
| `treaty source <symbol or file[:start-end]> [--all]` | Prints just that code, with line numbers. A file or range of more than 120 lines that is most of its file prints the file's outline, unless `--all` |
| `treaty impact <symbol or module>` | Lists everything that depends on the target, transitively |
| `treaty allowed <from> <to>` | Says whether one module may depend on another, and the rule. Modules may be ids, paths or short names |
| `treaty dump [--at <ref>]` | Prints the graph in AutoPen |
| `treaty design new <name> [--from <target>]...` | Scaffolds a design in `.treaty/designs/` from current contracts |
| `treaty design check <name>` | Compares a design with the code |
| `treaty map [--base <ref>] [--design <name>]...` | Renders a static map to `.treaty/out/map.html` |
| `treaty serve [--port <n>] [--open]` | Keeps this directory's live map open until interrupted |
| `treaty mcp [--port <n>]` | Relays an agent session over MCP on stdio to the treaty server, starting it when needed |
| `treaty restart [--port <n>]` | Stops the treaty server; open sessions start a new one and rejoin |
| `treaty init [--architecture <name>]` | Creates `.treaty/` and proposes a `treaty.yaml` for `none` (default), `hexagonal`, `clean`, `layered`, `slices` or `modular` |
| `treaty url` | Prints the live map's address for this directory |
| `treaty here [--force]` | Registers Treaty in this repository's `.mcp.json` |

## AutoPen

AutoPen (formerly CML) is Treaty's small diagram language and the model of
its graph. For code, AutoPen exists only in memory. People also write it by
hand in design files, `.treaty/designs/<name>.pen`, where they sketch new
modules and contracts before the code exists. As the code is written,
`treaty design check` compares the sketch with the code. Designs saved as
`.cml`, and documents that open with `cml 1`, still load.
See [AutoPen syntax proposal.md](AutoPen%20syntax%20proposal.md).

## Status

Treaty is early and changing fast. The contract graph, the five
architectures, the contract diff, context slices, designs, the live map and
the MCP server all work.

The full design, including goals, non-goals, acceptance criteria and open
questions, is in [Treaty design document.md](Treaty%20design%20document.md).

## License

MIT. See [LICENSE](LICENSE).
