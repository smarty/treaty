# Treaty

Treaty reads a codebase into a graph of its contracts, places them in the
architecture your team declares (hexagonal, clean, layered, vertical,
modular monolith or snowflake), and checks, slices and draws that graph. You can use it
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
  `go install` just works. Go, JavaScript, TypeScript (with Vue and Svelte
  components) and Python are supported.
- **Treaty passes its own checks.** The tool is hexagonal itself: a pure core
  with every side effect behind a port.

## Install

Each [release](https://github.com/smarty/treaty/releases) has a binary for
macOS and Linux, on Intel and ARM, with SHA-256 checksums.
`scripts/install.sh` installs one after checking its checksum, by default the
latest into `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/smarty/treaty/master/scripts/install.sh | sh -s v0.4.0
```

Or put `treaty` anywhere on your `PATH` yourself:

```sh
version=v0.4.0 os=darwin arch=arm64   # or linux, and amd64
curl -fsSLO "https://github.com/smarty/treaty/releases/download/$version/treaty_${version}_${os}_${arch}.tar.gz"
tar -xzf "treaty_${version}_${os}_${arch}.tar.gz"
mv "treaty_${version}_${os}_${arch}/treaty" /usr/local/bin/
```

The macOS binaries are not notarized. A file downloaded with `curl` runs as
is; one downloaded in a browser needs `xattr -d com.apple.quarantine treaty`
first.

Or build it with Go 1.27 or later:

```sh
go install github.com/smarty/treaty/cmd/treaty@latest
```

`treaty version` prints the version you have.

## Getting started

In the root of a Go repository:

```sh
treaty init --architecture layered   # propose a treaty.yaml from the import graph
treaty check --base main             # check the rules and contract changes; exits 1 on failure
treaty open                          # open the live map in your browser
```

`treaty init` fits the code to the architecture you name, using the shape of
the import graph. Directory names only act as hints. Without
`--architecture`, it writes `architecture: none`: no architecture, so every
dependency is allowed while contracts and their changes are still checked.
A repository with no `treaty.yaml` is treated the same way. Review and edit
the draft before you rely on it.

`treaty check` without `--base` checks only the architecture, and says so. Give
it the ref your work started from to classify contract changes too. With a
base, only new violations fail: one whose module edge already broke the rules
at the base is listed as existing, and drawn on the map with a hollow marker,
but passes. So a codebase that already breaks its rules can adopt Treaty
without fixing everything first, while the debt can't grow.

A hexagonal `treaty.yaml` looks like this:

```yaml
version: 1
architecture: hexagonal
layers:
  composition: ["cmd/**"]
  domain:      ["internal/graph/**", "internal/rules/**"]
  application: ["internal/app/**"]
  adapter:
    driving:   ["internal/adapters/cli/**", "internal/adapters/web/**"]
    driven:    ["internal/adapters/language/**", "internal/adapters/gitvcs/**"]
rules:
  fail_on: [breaking_changes, layer_violation, unclassified]
```

[configuration.md](configuration.md) is the full reference for the file:

- [Keys](configuration.md#keys): every key, its type, its default and the
  architectures that take it.
- [Globs](configuration.md#globs) and
  [how a module is placed](configuration.md#how-a-module-is-placed): what a
  pattern matches, and which glob wins when several do.
- [Rules](configuration.md#rules): what `fail_on` and `warn_on` count,
  including modules no glob places, and why only what a change introduced
  fails.
- [Versions](configuration.md#versions): the file's `version`, and why a
  misspelled key is an error rather than ignored.

## Architectures

Every architecture shares two rules: composition (usually `main`) may depend
on everything, and nothing may depend on composition.

| Architecture | Rules | Map |
| --- | --- | --- |
| `none` (default) | No architecture: every dependency is allowed. Contracts and their changes are still checked | One region holding every module |
| `hexagonal` | Dependencies point inward: domain ← application ← adapters. No adapter uses another adapter | Rings, with driving adapters on the left and driven on the right |
| `clean` | Dependencies point inward: entities ← use cases ← interface adapters ← frameworks | Four rings |
| `layered` | A layer may use itself and every layer below it. Skipping a layer is allowed; forbidding it is a team decision | Horizontal bands |
| `vertical` | A boundary may use itself and shared code, never another boundary. Optional layers inside every boundary | A column per boundary, or a grid of boundaries by layer |
| `modular` | A boundary may use its own packages, other boundaries' public packages and shared code. No cycles between boundaries | An island per boundary |
| `snowflake` | A star of stars: each star's core holds its contracts and orchestrators, and its other child directories are spokes. A spoke may use its own star's core and the cores around it, never a sibling spoke or anything in a sibling branch; a core never uses its own spokes | Each core ringed by its spokes, and each branch a smaller star in its parent's ring |

The layer architectures place modules in layers: their names are fixed for
`hexagonal` and `clean`, and yours for `layered`; see
[Layers](configuration.md#layers). `vertical` and `modular` divide code into
named [boundaries](configuration.md#boundaries), each gathering whatever
directories its globs match, so a feature spread across `web/orders` and
`store/orders` can still be one boundary:

```yaml
architecture: vertical
boundaries:
  orders:  ["**/orders/**"]
  billing: ["web/billing/**", "store/billing/**"]
```

`snowflake` names only [stars](configuration.md#stars), each a directory and
the globs of its core; every other child directory of a star is a spoke,
found without being listed. Orchestrators in a core reach their spokes only
through ports declared there, and composition hands the implementations in,
as in the Presentation-Abstraction-Control and Mediator patterns, applied
recursively:

```yaml
architecture: snowflake
composition: ["cmd/**", "internal/factory/**"]
stars:
  internal:        ["internal/core/**"]
  internal/blocks: ["internal/blocks/core/**"]
```

A sideways dependency accepted for now is an entry under
[`rules.ignore`](configuration.md#ignoring-a-finding), with its reason, like
any other violation.

Treaty reads the whole repository, including sub-trees with their own
`go.mod`. Their imports resolve through their own module paths, and imports
between them and the rest of the repository count like any other.

## The live map

One treaty server runs on your machine and holds the graph of every
repository you work in. Its one page, `http://127.0.0.1:7878/`, has a tab for
each open repository, so a single browser tab follows them all;
`http://127.0.0.1:7878/?p=<folder>` opens with that repository's tab shown. It
watches each working tree and pushes
every rebuild to the browser. `treaty open` opens the current directory's map,
or `treaty open <dir>` another's, without an agent; Claude Code sessions open
it too (see below). Whichever starts the server opens a browser on the map,
unless a page already shows it. Each tab shows 🧠 left of its name while an
agent works in it through MCP and 📄 while none does, and then × on its right:
closing the tab closes the project, and closing the last one stops the
server. Opening a repository writes nothing to it: no `treaty.yaml` appears
unless you change the architecture, and an edit that changes nothing leaves
the file alone, so you can review any repository without leaving Treaty
behind in `git status`. The map lays modules
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
and agents keep following `treaty.yaml`, and the file is never touched,
until you press *Use this architecture*. Then `treaty.yaml` is replaced
with the proposal.

The review queue and the inspector are tabs in drawers beside the map. Drag
a divider to resize a drawer. Drag a tab to move its panel into another
drawer, split a drawer, join the map as a tab, or float it over the map;
drop it on a tab bar to put it among those tabs, which also reorders them.
The map's views and the project tabs reorder the same way, but don't dock.
Tabs that don't fit scroll sideways: use the wheel or the arrows at either
end. *Reset layout*, beside the Theme menu at the top, restores the default.
*Show internals* and *Follow Claude* sit at the end of the map's view bar
and belong to each project's map.
Click a file's cell to select it, just like a module or symbol: the
inspector shows the file's code and its agent context slice, and a symbol
shows its documentation and code. Within a file, symbols run values, types,
interfaces, methods, then functions, each by name: the contracts clockwise
from the left end of the file's bracket on the border, and the internals
across the file's cell. A package that holds a `go.mod` shows it in the first
cell of its ring, at 12:00; click it to read the file.
In `snowflake`, each star's core sits at its center with its spokes on a ring
around it, and a branch is a smaller star in its parent's ring. Allowed
dependencies run inward in lanes, and a sideways one bows outward between
arms as a violation. A star's menu has *Zoom to this star*.
Directories with several packages are drawn as groups, which collapse when
they are small on screen. Select a group to see its modules measured as one:
its coupling across its edge, instability, abstractness and distance, and the
modules outside it that it uses and that use it. Groups never change the
rules, which apply to each package.
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
  classifies it.
- **In another boundary, shared or composition** (`vertical` and `modular`):
  Treaty adds the module's exact path to that boundary's globs, or to
  `shared` or `composition`, the same way. Its layer inside a boundary still
  comes from `layers`, and a move never creates a boundary.
- **Into or out of a core, or to composition** (`snowflake`): Treaty adds the
  module's exact path to the core of a star that holds it, takes it out
  again so it returns to its own directory's spoke, or adds it to
  `composition`. A spoke is its directory, so moving a module to another
  spoke means moving its files.

Moves wait while another architecture is previewed.

The **Terms** tab, beside the inspector, holds the whole `treaty.yaml` for
editing. Save checks it the way `treaty check` reads it, so a file that
would not load is refused with its line marked, and it won't overwrite a
change made on disk since you opened it. Select a module and the tab marks
the line of each glob that places it, which the inspector also lists under
*Placed by*. See [Editing from the map](configuration.md#editing-from-the-map).

Above the review queue, the map says what `treaty check` would against the
baseline: what fails the check and what warns. Each finding that counts is
tagged *fails the check* or *warns*. The queue's tabs are **All**, the default,
which labels each item with the tab it belongs to; **New**, what the change
brings; **Existing**, what the baseline already had; and **Ignored**. *Ignore…* on
an item asks why and adds it to `rules.ignore` in `treaty.yaml`, so it
neither fails nor warns until it changes; then it returns to New, marked as
ignored before. See [Ignoring a finding](configuration.md#ignoring-a-finding).

Right-click a band, boundary or area (`layered`, `vertical`, `modular` and
`none`) for its menu: *Rename…* gives it another name on this map,
*Restore the name* takes it back, *Zoom to this region* fits it in view, and
*Put its moved modules back* returns the modules moved inside it. Names are
for the map only, saved for this repository and architecture in
`.treaty/map.json`; `treaty.yaml`, the rules and the checks keep the
region's own name. A boundary has *Rename boundary…* instead, which renames it
in `treaty.yaml`, keeping its globs and comments.

The Theme menu, at the top beside the project tabs, offers System (following your OS) and 21 themes: Light and
Dark in the style of VS Code's defaults; High Contrast and Color-blind Safe
versions of each for accessibility; and Dawn, Dusk, Fjord, Vampire, Harvest,
Blossom, Great Wave, Gumdrop, Gumdrop Light, Neon Rain, Comic, Midnight, DOS
(dark), DOS (light) and Windows 3.x.
Themes are JSON files in `~/.treaty/themes/`. Treaty rewrites its own there
every time a server starts, so copy one to a new name to make your own; any
other `.json` file there shows up in the menu under Yours.

Your theme and layout are saved in `~/.treaty/settings.json`, so they
follow you across repositories, browsers and sessions. Each map's *Show
internals*, *Follow Claude* and region names are saved with its repository,
in `.treaty/map.json`.

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
| `allowed` | Whether one module may depend on another, before an import is added; in `snowflake`, a refusal names the ports in a core that the target already implements |
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
| `treaty check [--base <ref> \| --merge-base <branch>] [--format json\|text\|summary\|markdown]` | Runs every check on the working tree, and on its diff from base when one is given; exits 1 on failure. `--merge-base main` compares with where HEAD left `main`, as a pull request into it does. `summary` lists only the verdict, notes, violations and breaking changes, and counts the rest; `markdown` is the report a pull request shows |
| `treaty ci [--target <branch>]` | Checks a pull request against its merge base with the target, writes its map, and shows the check on the pull request in GitHub Actions or Bitbucket Pipelines; exits 1 on failure. See [ci.md](ci.md) |
| `treaty overview [--base <ref>]` | Prints every module by layer and every module dependency, in a few kilobytes |
| `treaty find <query> [--kind <kind>]` | Lists symbols and struct fields whose id contains the query, with file:line and signature |
| `treaty slice <target> [--format text\|json]` | Prints a context slice, one line per entry with file:line; `--format json` for the full form |
| `treaty source <symbol or file[:start-end]> [--all]` | Prints just that code, with line numbers. A file or range of more than 120 lines that is most of its file prints the file's outline, unless `--all` |
| `treaty impact <symbol or module>` | Lists everything that depends on the target, transitively |
| `treaty allowed <from> <to>` | Says whether one module may depend on another, and the rule. Modules may be ids, paths or short names |
| `treaty dump [--at <ref>]` | Prints the graph in AutoPen |
| `treaty design new <name> [--from <target>]...` | Scaffolds a design in `.treaty/designs/` from current contracts |
| `treaty design check <name>` | Compares a design with the code |
| `treaty map [--base <ref> \| --merge-base <branch>] [--design <name>]...` | Renders a static map to `.treaty/out/map.html` |
| `treaty open [<dir>] [--port <n>]` | Opens this directory's live map, or dir's, without an agent, until you close its tab; starts the treaty server, and a browser when no page shows it |
| `treaty serve [--port <n>] [--open]` | Opens this directory's live map until interrupted |
| `treaty mcp [--port <n>]` | Relays an agent session over MCP on stdio to the treaty server, starting it when needed |
| `treaty restart [--port <n>]` | Stops the treaty server; open sessions start a new one and rejoin |
| `treaty init [--architecture <name>]` | Creates `.treaty/` and proposes a `treaty.yaml` for `none` (default), `hexagonal`, `clean`, `layered`, `vertical`, `modular` or `snowflake` |
| `treaty url` | Prints the live map's address for this directory |
| `treaty here [--force]` | Registers Treaty in this repository's `.mcp.json` |
| `treaty version` | Prints this build's version: the release's, or for a build from source the module version Go recorded |

## In CI

`treaty ci` checks a pull request against its merge base with the target
branch, annotates each finding that fails or warns on its line, shows the
report on the pull request and keeps the map of the change. On GitHub:

```yaml
- uses: actions/checkout@v7
  with:
    fetch-depth: 0
- uses: smarty/treaty@v0.4.0
```

On Bitbucket Pipelines it publishes a Code Insights report, with no token
needed. [ci.md](ci.md) has both setups, and what to do when a clone is too
shallow to compare.

## AutoPen

AutoPen (formerly CML) is Treaty's small diagram language and the model of
its graph. For code, AutoPen exists only in memory. People also write it by
hand in design files, `.treaty/designs/<name>.pen`, where they sketch new
modules and contracts before the code exists. As the code is written,
`treaty design check` compares the sketch with the code. Designs saved as
`.cml`, and documents that open with `cml 1`, still load.
See [AutoPen syntax proposal.md](AutoPen%20syntax%20proposal.md).

## Status

Treaty is early and changing fast. The contract graph, the six
architectures, the contract diff, context slices, designs, the live map and
the MCP server all work.

The full design, including goals, non-goals, acceptance criteria and open
questions, is in [Treaty design document.md](Treaty%20design%20document.md).

## Releasing

CI runs `gofmt`, `go vet` and the tests on macOS and Linux for every push to
`master` and every pull request, and checks each pull request with Treaty
itself, through `action.yml` built from source. The tests include one that parses the live
map's script with Node, since the page has no build step to catch a syntax
error.

Before a release, compare the new build with the last one across the
repositories you have, so a scanner change can't quietly shift what Treaty
reads:

```sh
go build -o /tmp/treaty-new ./cmd/treaty
scripts/compare.sh "$(command -v treaty)" /tmp/treaty-new ~/src/github.com/smarty/*/
```

It diffs `overview` and `dump` for each repository and lists the ones that
differ. Then tag and push:

```sh
git tag -a v0.2.0 -m "Treaty v0.2.0"
git push origin v0.2.0
```

The release workflow runs the tests again, builds the archives with
`scripts/release.sh`, which stamps the version into the binary, and publishes
them with their checksums as a GitHub release. A tag with a suffix, such as
`v0.2.0-rc.1`, publishes a pre-release. `scripts/release.sh v0.2.0` builds the
same archives locally, in `dist/`.

## License

MIT. See [LICENSE](LICENSE).
