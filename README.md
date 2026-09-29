# Treaty

Treaty reads a codebase into a graph of its contracts, places them in
hexagonal architecture layers, and checks, slices and draws that graph. You
can use it for code review, for thinking through a design, and alongside a
coding agent.

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
  breaking changes and layer violations first.
- **Layers are declared and checked.** `treaty.yaml` assigns each module to
  a hexagonal layer: domain, application, adapter (driving or driven), or
  composition. A dependency may point inward or sideways, never outward.
  Treaty fails the check when a dependency breaks that rule.
- **Every score is mechanical.** Scores come from static analysis or test
  execution, never from a model's judgment. There are no AI-generated
  scores, summaries or risk labels anywhere in the scoring path, so every
  result is reproducible.
- **Ephemeral by design.** The graph lives in memory. Treaty doesn't
  communicate a design between people or keep it as a record. Nothing it
  writes is ever tracked in git. Its workspace, `.treaty/`, ignores itself.
- **Give agents only what they need.** A context slice holds the contracts
  around one symbol or module and the layer rules it must not break, and
  counts what it left out. An agent can work on the right code without
  reading the whole repository.
- **Small, hand-written scanners.** Treaty needs contracts, not a full
  parse tree. Each language gets a small hand-written contract scanner
  behind one extractor interface. The tool is pure Go with no cgo, so
  `go install` just works. Go is the only supported language for now.
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
treaty init     # create .treaty/ and propose a treaty.yaml from the import graph
treaty check    # check layer rules and contract changes; exits 1 on failure
treaty serve --open
```

`treaty init` works out the layers from the shape of the import graph, so
its proposal has no violations. Directory names only act as hints. Review
and edit the draft before you rely on it.

A `treaty.yaml` looks like this:

```yaml
layers:
  composition: ["cmd/**"]
  domain:      ["internal/graph/**", "internal/rules/**"]
  application: ["internal/app/**"]
  adapter:
    driving:   ["internal/adapters/cli/**", "internal/adapters/web/**"]
    driven:    ["internal/adapters/golang/**", "internal/adapters/gitvcs/**"]
rules:
  fail_on: [breaking, layer_violation]
  warn_on: [unclassified, weak_contract]
  strength_threshold: 0.60
```

## The live map

`treaty serve` runs a server that holds the graph in memory, watches the
working tree, and pushes each rebuild to the browser. The map shows modules
in their hexagonal rings, with changes shown against a baseline: HEAD for
uncommitted work, the merge base for a pull request, or any ref. You can
change the baseline from the map's header while it runs. The server listens
on `127.0.0.1` only, on port 7878 by default.

## Working with Claude Code

```sh
treaty here
```

This registers Treaty in the repository's `.mcp.json`. Each Claude Code
session started in the repository then runs `treaty mcp`, which serves the
same live graph to the agent as MCP tools, and serves the map to you if no
map is already running. You and the agent always look at the same graph:

| Tool | Answers |
| --- | --- |
| `overview` | The whole architecture in a few kilobytes: modules by layer, contract counts, module edges |
| `find` | Symbols by name or kind, with id, file, line and signature |
| `slice` | The context for working on one symbol or module |
| `impact` | Everything that depends on a symbol, transitively, before an edit |
| `allowed` | Whether one module may depend on another, before an import is added |
| `changes` | What changed in the architecture since the baseline, including new violations |
| `plan` | The agent's intended contracts in CML, drawn on the map as unbuilt and filled in as the code is written |
| `selection` | What the person has selected on the map, so they can point and say "work here" |
| `show` | Asks the person to look at one thing. It is an offer on the map, never a forced move, and limited to once every 15 seconds |
| `check`, `design_check` | The same checks as the CLI |

## Commands

| Command | Does |
| --- | --- |
| `treaty check [--base <ref>] [--format json\|text]` | Runs every check on the diff from base to HEAD; exits 1 on failure |
| `treaty dump [--at <ref>]` | Prints the graph in CML |
| `treaty slice <symbol or module>` | Prints a context slice as JSON |
| `treaty design new <name> [--from <target>]...` | Scaffolds a design in `.treaty/designs/` from current contracts |
| `treaty design check <name>` | Compares a design with the code |
| `treaty map [--base <ref>] [--design <name>]...` | Renders a static map to `.treaty/out/map.html` |
| `treaty serve [--port <n>] [--open]` | Serves the live map until interrupted |
| `treaty mcp [--port <n>]` | Serves the live graph over MCP on stdio, plus the live map |
| `treaty init` | Creates `.treaty/` and proposes a `treaty.yaml` |
| `treaty url` | Prints the live map's address for this directory |
| `treaty here [--force]` | Registers Treaty in this repository's `.mcp.json` |

## CML

CML is Treaty's small diagram language and the model of its graph. For code,
CML exists only in memory. People also write it by hand in design files,
where they sketch new modules and contracts before the code exists. As the
code is written, `treaty design check` compares the sketch with the code.
See [CML syntax proposal.md](CML%20syntax%20proposal.md).

## Status

Treaty is early and changing fast. The contract graph, layer checks,
contract diff, slices, designs, the live map and the MCP server all work.
Contract-scoped mutation testing is still to come: it will score how well
tests that enter through each contract kill mutants. Until then, test
strength is reported as not computed.

The full design, including goals, non-goals, acceptance criteria and open
questions, is in [Treaty design document.md](Treaty%20design%20document.md).

## License

MIT. See [LICENSE](LICENSE).
