# treaty.yaml reference

`treaty.yaml`, at the repository root, declares the architecture Treaty checks:
which modules sit where, and which findings fail a check. `treaty init` writes
a first draft from the import graph; edit it before you rely on it. A
repository without the file has no architecture: every dependency is allowed,
and contracts and their changes are still checked.

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

## Keys

| Key | Type | Architectures | Default | Meaning |
| --- | --- | --- | --- | --- |
| `version` | whole number | all | `0` | The format of the file. See [Versions](#versions). |
| `architecture` | `none`, `hexagonal`, `clean`, `layered`, `vertical`, `modular` or `snowflake` | all | `none` | The rules dependencies must follow. See [Architectures](#architectures). |
| `composition` | list of globs | all but `none` and `snowflake` | none | Modules that wire everything together, such as Go `main` packages. Composition may use every module; nothing may use composition. In `hexagonal`, it may also go under `layers`. In `snowflake`, assemblers do this work. |
| `layers` | map of layer name to list of globs | `hexagonal`, `clean`, `layered`, `vertical` | none | The layers and the modules in each. See [Layers](#layers). |
| `layers.adapter.driving`, `layers.adapter.driven` | list of globs | `hexagonal` | none | The two sides of the adapter layer: adapters that call into the core, such as a CLI or web handlers, and adapters the core calls, such as storage. |
| `shared` | list of globs | `vertical`, `modular` | none | Code every boundary may use. Shared code may use only shared code. |
| `boundaries` | map of boundary name to list of globs | `vertical`, `modular` | none; at least one is required | Named groups of modules that are kept apart. See [Boundaries](#boundaries). |
| `public` | list of globs | `modular` | the top of each boundary | The modules other boundaries may use. See [Boundaries](#boundaries). |
| `stars` | map of star directory to `contracts`, `assemblers` and `engines` | `snowflake` | none; `.` is required | Each star's directory, and the directories of its contracts, its assemblers and its engines. Spokes are never listed: they are the star's other children that hold a package. See [Stars](#stars). |
| `stars.<dir>.contracts` | list of directories | `snowflake` | `[]` | The star's contracts packages. |
| `stars.<dir>.assemblers` | list of directories | `snowflake` | `[]` | The packages that compose the star: they may wire its leaves. |
| `stars.<dir>.engines` | list of directories | `snowflake` | `[]` | The packages that run policy over contracts only. |
| `stars.<dir>.orchestrators` | list of directories | `snowflake` | `[]` | Deprecated: the second snowflake's name for `assemblers`, read as `assemblers` with a note. A star may not list both. |
| `rules.fail_on` | list of rule names | all | `[breaking_changes, layer_violation, dependency_cycles]`, and `impure_contracts` for `snowflake` | Findings that fail `treaty check`. See [Rules](#rules). |
| `rules.warn_on` | list of rule names | all | `[unclassified, ambiguous_boundaries]`, and `impure_contracts` for `snowflake` | Findings that `treaty check` reports as warnings without failing. A rule in both lists fails. |
| `rules.tests` | `check` or `skip` | all | `check` for `snowflake`, `skip` for the others | Whether test files' imports are checked, by the rules of the package they test. See [Test files](#test-files). |
| `rules.ignore` | list of entries, each with `finding`, `target`, `fingerprint` and `reason` | all | none | Single findings that neither fail nor warn, each with the reason. See [Ignoring a finding](#ignoring-a-finding). |

Every other key is an error.

## Architectures

| `architecture` | Rule | Keys it takes |
| --- | --- | --- |
| `none` | No rules: every dependency is allowed. | `rules` only |
| `hexagonal` | Dependencies point inward: domain ← application ← adapters. No adapter uses another adapter. | `composition`, `layers` with `domain`, `application` and `adapter` |
| `clean` | Dependencies point inward: entities ← use cases ← interface adapters ← frameworks. | `composition`, `layers` with `entities`, `use_cases`, `interface_adapters` and `frameworks` |
| `layered` | A layer may use itself and every layer below it. | `composition`, `layers` with names of your own, at least one |
| `vertical` | A boundary may use itself and shared code, never another boundary. | `composition`, `shared`, `boundaries`, and optionally `layers` |
| `modular` | A boundary may use its own modules, the public modules of other boundaries, and shared code. Boundaries may not depend on each other in a cycle. | `composition`, `shared`, `boundaries`, `public` |
| `snowflake` | The code is a star of stars, each with a core of contracts, assemblers and engines. Contracts use only contracts of their own star and its ancestors. An engine uses those contracts and its own star's engines. An assembler wires its star's core and spokes and each child branch's core. A spoke uses its own leaf, its star's contracts and engines and every ancestor's contracts, and never an assembler. Nothing runs sideways, not even between the cores of sibling branches. | `stars` |

In every architecture but `none` and `snowflake`, composition may use
everything and nothing may use composition. A snowflake has no composition:
main packages, factories and the public API are assemblers in the core of
their star.

## Layers

`layers` maps each layer's name to the globs of its modules.

- **`layered`**: the names are yours, listed top to bottom. Each layer may
  use itself and every layer below it, so skipping a layer is allowed.
- **`hexagonal`** and **`clean`**: the names are fixed, and their order in
  the file does not matter. `adapter` holds a map with `driving` and
  `driven` instead of a list.
- **`vertical`**: layers are optional. They run top to bottom inside every
  boundary, and each may use itself and the layers below it. Their globs
  are relative to the repository root like every other glob, so a layer
  usually names a directory at any depth, such as `api: ["**/api/**"]`.

`composition`, `shared`, `unclassified`, `boundary` and `none` are reserved and
can't be layer names. The snowflake's `contracts`, `assembler`, `engine` and `leaf`
are not reserved, so a `layered` file may still name a layer `contracts`.

## Boundaries

`boundaries` maps each boundary's name to the globs of its modules. A boundary
can span directories:

```yaml
version: 1
architecture: modular
composition: ["cmd/**"]
shared:      ["internal/platform/**"]
boundaries:
  orders:  ["**/orders/**"]
  billing: ["web/billing/**", "store/billing/**"]
public: ["**/api/**"]
rules:
  fail_on: [breaking_changes, layer_violation, dependency_cycles, unclassified, ambiguous_boundaries]
```

The name appears in rule messages, such as "boundary orders may not depend on
boundary billing", and on the map.

In `modular`, a module is public, so other boundaries may use it, when it
matches a `public` glob. Without `public`, the top of each boundary is public:
the modules a boundary's glob matches with its trailing `/**` removed. So
`web/billing/**` makes `web/billing` public, and `**/orders/**` makes every
directory named `orders` public.

## Stars

`snowflake` sees the repository as a recursive star. Each star is a
directory, and each has a **core**: a region of the map, not a package,
holding three kinds of package, each named for what it does:

- its **contracts**: interfaces, value types and their pure methods, errors
  and constants;
- its **assemblers**, which compose: its public API, an assembly or
  factory, a kit, a main package that builds its leaves;
- its **engines**, which run policy over contracts only: an executor, a
  pipeline of ports, a scheduler.

No package needs to be named `core`. Every other child of a star that holds
a package is a **spoke**: a **leaf**, with every package nested in it, or a
**branch**, a star of its own with its own core and spokes.

```yaml
version: 1
architecture: snowflake
stars:
  .:
    contracts:  ["internal/contracts"]
    assemblers: [".", "cmd/guardprobe", "internal/assembly"]
    engines:    ["internal/executor"]
  internal/blocks:
    contracts:  ["internal/blocks/contracts"]
    assemblers: ["internal/blocks/kit"]
  internal/gate:
    contracts:  ["internal/gate/contracts"]
    assemblers: ["internal/gate/pipeline"]
rules:
  fail_on: [breaking_changes, layer_violation, unclassified, impure_contracts]
  ignore: []
```

Here `.`, the repository, is the root star. Its contracts are
`internal/contracts`. Its assemblers are the root package, the public API;
`cmd/guardprobe`, a main package; and `internal/assembly`, which builds the
root's leaves and asks each branch's assembler for what that branch builds.
Its engine is `internal/executor`, which runs plans through the ports in
`internal/contracts`. `internal` and `cmd` hold no package, so placement
looks through them: every other package under them, such as
`internal/plan` or `internal/web`, is a leaf of `.`. `internal/blocks` and
`internal/gate` are branches. Inside `internal/blocks`,
`internal/blocks/contracts` holds what the blocks share, `internal/blocks/kit`
builds every block, and each block, such as `internal/blocks/www`, is a leaf
of that star.

A smaller tree shows the other ways to write a star: on one line, or with no
value at all, a core with nothing in it yet.

```yaml
version: 1
architecture: snowflake
stars:
  .:
    contracts:  ["src/contracts"]
    assemblers: ["src/app"]
  src/billing: {contracts: ["src/billing/contracts"], assemblers: ["src/billing/assembly"], engines: ["src/billing/ledger"]}
  src/reports:   # nothing wires its spokes until its core has packages
```

Any of a star's three lists may be empty or missing. `orchestrators`, the
second snowflake's name for assemblers, is still read as `assemblers`, and
the check notes it, so it can be renamed; move each package that only runs
policy over contracts to `engines` as you rename.

### What may use what

These dependencies are allowed, and nothing else. S is the innermost star
holding the module. Its ancestors are S's parent, the nearest star around
it, the parent's parent, and so on up to `.`. A branch's core is a child
star's contracts, assemblers and engines.

| A module in star S that is | May use |
| --- | --- |
| contracts | S's contracts; the contracts of every ancestor |
| an engine | S's contracts and every ancestor's; S's engines |
| an assembler | S's whole core; S's spokes, to wire them; the core of each child branch; the contracts of every ancestor |
| a spoke (a leaf) | its own leaf, every package nested in it included; S's contracts and engines; the contracts of every ancestor |

So:

- **Contracts use only contracts of their own star and its ancestors.**
  Never a spoke, an engine or an assembler, not even one of their own star,
  and never a branch's contracts: what a branch and its parent share
  belongs in the parent's contracts.
- **An engine runs policy over contracts only.** It uses contracts and its
  own star's engines, never a spoke or an assembler: an assembler hands it
  the implementations.
- **An assembler composes.** It wires its star's leaves, uses its star's
  core and each branch's core, never a branch's spokes, and of the stars
  around it only their contracts.
- **A spoke never uses an assembler,** not its own star's and not any
  other. So no leaf can reach a sibling through an assembler that exposes
  it.
- **A spoke uses ancestor contracts directly.** A leaf of `internal/blocks`
  may use `internal/contracts`; there are no re-exports to keep in step.
  A leaf declares the interfaces it consumes, or uses a port in the
  contracts of its star or one around it.
- **Nothing runs sideways.** Spokes never use each other, sibling branches'
  cores never use each other, and nothing reaches into another star but as
  above.
- **Dependency inversion is the mechanism.** A spoke depends on interfaces
  in contracts, and an assembler builds the implementations and hands them
  in. A Go spoke may also declare its own small interface that a sibling's
  type satisfies without either importing the other: Treaty sees no
  dependency between them, and the assembler hands one to the other.
- **Main packages** are assemblers and are listed.

The rules do not rule out every cycle between regions: an assembler wires a
leaf, and in TypeScript, JavaScript and Python a leaf importing it back
compiles, though the snowflake reports that edge, since a spoke never uses
an assembler. In C#, one project builds with a cycle between its
directories, which the snowflake reports the same way, while MSBuild
refuses a cycle of project references. Go refuses any cycle of packages
when it builds;
`dependency_cycles` finds cycles only between the boundaries of a `modular`
architecture. The snowflake's dependency violations are `layer_violation`s,
and each names the rule it breaks and what to do, such as:

```
leaf internal/plan may not depend on assembler internal/assembly: a spoke never uses an assembler: an assembler hands a spoke what it needs; declare a port in the contracts of star . (internal/contracts), implement it in internal/assembly, and let an assembler of star . hand it in
contracts internal/contracts may not depend on contracts internal/blocks/contracts: contracts use only contracts of their own star and its ancestors; move what it needs into the contracts of star . (internal/contracts), which both may use
leaf internal/plan may not depend on leaf internal/web: internal/plan and internal/web are sibling spokes of star ., and no dependency runs sideways; declare a port in the contracts of star . (internal/contracts), implement it in internal/web, and let an assembler of star . hand it in
assembler internal/assembly may not depend on leaf internal/blocks/www: an assembler reaches a child branch only through its core, and internal/blocks/www is a spoke of branch internal/blocks; let an assembler of star internal/blocks (internal/blocks/kit) build it, and use that
```

A dependency that breaks these rules is accepted only as an entry under
`rules.ignore`, with its reason (see
[Ignoring a finding](#ignoring-a-finding)).

### Pure contracts

A snowflake's contracts are checked for purity, reported as
`impure_contracts`, which fails by default. A contracts package:

- imports only the standard library, and of it never `net` or anything
  under it, `os/exec`, `os/signal`, `syscall`, `unsafe`, `plugin` or cgo's
  `C`, and never a third-party module;
- declares no package-level variable, but the blank identifier `_` and
  sentinel errors: a name starting with `Err` whose value is a call of
  `errors.New` or `fmt.Errorf`.

Each impurity is its own finding, its target the package and what it holds,
such as `go:internal/contracts: var Registry` or
`go:internal/contracts: import net/http`, so an exception is one entry under
`rules.ignore`. Purity is read from Go; other languages' contracts are
checked only by the import rules.

### Test files

Treaty reads Go test files, internal and external test packages alike, and
checks their imports by the rules of the package they test, so a test of a
leaf may not reach a sibling either. Such a violation is marked as coming
from a test (`in a test: ...`), and its finding's target ends in `(test)`,
so it can be ignored apart from the package's own dependency on the same
module. `rules.tests: skip` leaves tests unchecked; it is `check` by default
in a snowflake and `skip` in every other architecture.

### What the check sees

- **Every built file.** Only a Go file under `//go:build ignore` is left
  out; one under any other constraint, even `tools` or one no build
  satisfies, is read.
- **Hidden packages.** Treaty reads no directory starting with `_` or `.`,
  and no `vendor` or `testdata`, yet Go builds such a package when code
  imports it. Each import of one, from code or a test, is reported as
  `unclassified`, naming the importer, so its code and dependencies cannot
  slip past the rules.
- **`//go:linkname`.** Every such directive links to a symbol past every
  import, so it is a `layer_violation` naming its target, in every
  architecture but `none`.

### Writing stars

- **A star is a directory**, relative to the repository root, never a glob:
  `.`, `internal/blocks`. `internal/` and `./internal` mean `internal`. `.`,
  the repository, is the root star and must be listed. Every other star's
  parent is the nearest listed star around it, and the directories between
  them must hold no package.
- **Entries are directories**, relative to the repository root like every
  path in the file, so `internal/blocks` lists `internal/blocks/contracts`,
  not `contracts`, and never globs, so a new directory stays a spoke until
  the file names it. `*` and `?` are refused, since a directory name cannot
  hold them on Windows; brackets are read as they are, so a file-routed
  directory such as `app/[slug]` can be an entry or a star.
- **An entry names exactly one package**: the star's own, or one below the
  star, reached through directories that hold no package or through other
  entries. A package beneath an entry is **unclassified**, and noted,
  unless an entry names it too, in any role: `internal/blocks/kit/sub`
  under the assembler `internal/blocks/kit` must be listed itself. The
  exception is a star's own directory: the packages under it are its
  spokes, whatever role its own package has.
- **Each directory is listed once**: under one star, in one of the three
  lists. An entry may not lie outside its star, inside a branch, whose core
  is its own, around a star, or inside a spoke.
- **A star's own package must be listed** once its directory holds code, in
  one of its lists. Until then it is unclassified.
- **Spokes are found, never listed.** A new directory under a star is a leaf
  the moment it holds code, so it is checked at once and cannot hide a
  sideways dependency by being left out of the file. Turning a leaf into a
  branch is one entry under `stars`.
- **The stars must fit the code.** A package in a directory placement looks
  through, such as an `internal/doc.go` above the branch `internal/blocks`,
  fails the check with the misfit, since it would make `internal` a leaf
  holding the branch. So does an entry that holds no package of its own
  while packages lie below it, such as `internal/stuff` holding only
  `internal/stuff/a` and `internal/stuff/b`: name each package. A base
  revision that does not fit is still compared, with its misfit
  directories looked through.

A star or entry that matches no module at all, such as a misspelled one, a
star whose own package is not listed, a star with leaves but an empty core,
a star that still says `orchestrators`, and a package beneath a core
package that no entry names are named in the check's notes.

`composition`, `layers`, `shared`, `boundaries` and `public` are errors in
`snowflake`, and `stars` is an error in every other architecture:

```
treaty.yaml: line 3: unknown key "starz"; did you mean "stars"?
treaty.yaml: line 6: unknown key "core" under stars.internal; a star maps contracts, assemblers and engines
treaty.yaml: line 7: assemblers is listed twice under stars.internal/blocks, first at line 6
treaty.yaml: line 5: invalid architecture: stars."." lists both assemblers and orchestrators, the earlier name for assemblers; merge orchestrators into assemblers
treaty.yaml: line 4: invalid architecture: stars."." is a list, the earlier shape of a star; split its entries into contracts, assemblers and engines, such as .: {contracts: ["internal/contracts"], assemblers: ["."]}
treaty.yaml: line 5: invalid architecture: stars.internal/blocks.engines must be a list of directories, such as engines: ["internal/blocks/engine"]
treaty.yaml: invalid architecture: stars apply only to the snowflake architecture
treaty.yaml: invalid architecture: composition does not apply to the snowflake architecture: assemblers do its work, so list main packages, factories and the public API among the assemblers of their star
treaty.yaml: invalid architecture: the snowflake's root star is the repository itself: declare "." under stars, around internal/blocks and internal/gate
treaty.yaml: invalid architecture: assemblers entry "cmd/*" of star . is a glob; name each package by its directory, so a new directory stays a spoke until treaty.yaml names it
treaty.yaml: invalid architecture: "run" is listed under both the assemblers and the engines of star .; a core package has one role
treaty.yaml: invalid architecture: contracts entry "internal/blocks/contracts" of star . lies inside branch internal/blocks, whose core is its own; list it under internal/blocks
treaty.yaml: invalid architecture: the stars do not fit the code: star internal/blocks lies inside spoke internal of star .: internal holds a package, so it is a leaf holding everything below it; declare internal a star, or move its package into a child directory
treaty.yaml: invalid architecture: the stars do not fit the code: assemblers entry internal/stuff of star . holds no package of its own, only internal/stuff/a and internal/stuff/b below it; name each package, so a new one stays a spoke until treaty.yaml names it
```

The snowflake is newer than Treaty v0.4.0. Until the next release, build
Treaty from source (`go install github.com/smarty/treaty/cmd/treaty@master`),
or run the GitHub action with `version: source` (see [ci.md](ci.md)).

### Prior art

The snowflake belongs to an old genus: recursive architectures of components
wired by the component around them.

- **Presentation-Abstraction-Control** (Coutaz, 1987) and **Hierarchical
  MVC** (Cai, Kapila and Pal, 2000) arrange a system as a hierarchy of
  agents or triads that talk only through their parent.
- **Darwin** (Magee, Dulay, Eisenbach and Kramer, 1995) and **Fractal**
  (Bruneton, Coupaye, Leclercq, Quéma and Stefani; ObjectWeb, now OW2)
  describe composite components whose bindings are declared by the
  component that holds them.
- **Koala** (van Ommering, van der Linden, Kramer and Magee, Philips, IEEE
  Computer, 2000) is the closest relative: compound components with
  provides and requires interfaces, bound by the component around them, and
  checked when the product is built.
- Within one level, the **Mediator** pattern (Gamma, Helm, Johnson and
  Vlissides, 1994), whose colleagues talk only through the mediator, and
  **hexagonal ports and adapters** (Cockburn, 2005), whose ports are
  declared inward and whose implementations are handed in.

The snowflake is its own species. It has no component language: it is a
check over plain imports in Go, TypeScript, JavaScript and Python, and over
the references between directories in C#, where a `using` names a
namespace rather than a module. It puts a
core region, with contracts, assemblers and engines, at every level; it lets
a spoke use the contracts of every star around it, never an assembler; it
allows no sideways dependency, not even between the cores of sibling
branches; it leaves the wiring in code, in assemblers; and its exceptions
are named edges, each with a reason.

## Globs

Every glob is matched against a module's path, relative to the repository
root. A module is a directory of source: a Go package, a directory of
JavaScript or TypeScript files, a Python package, or a directory of C#
files. The repository root's
path is `.`.

Test files are not read in any architecture, so their imports are never
checked: Go `_test.go` files; JavaScript and TypeScript `.test.` and
`.spec.` files and `__tests__` directories; Python `test_*.py`, `*_test.py`
and `conftest.py` files and `tests` directories; and every file of a C# test
project, one that references a test framework such as xUnit, NUnit, MSTest
or TUnit, sets `IsTestProject`, uses `MSTest.Sdk`, or has a name ending in
`.Tests`, `.UnitTests`, `.IntegrationTests` or `.FunctionalTests`. Keep tests within the rules
by convention. Treaty reads Go tests only to find which tests exercise which
contracts, never as dependencies.

| Pattern | Matches |
| --- | --- |
| `**` | Any number of path segments, including none. `web/**` matches `web` and everything under it. |
| `*` | Any run of characters inside one segment. `internal/*` matches `internal/orders` but not `internal/orders/db`. |
| `?` | One character inside one segment. |
| `[abc]`, `[a-z]` | One character from the set or range. |
| anything else | Itself. |

## How a module is placed

A module is placed once, by the first of these steps that matches it.

**`hexagonal`, `clean` and `layered`:**

1. A glob that is exactly the module's path, in `composition` or a layer.
   This lets one module leave a broad glob, such as moving
   `internal/web/admin` out of `internal/web/**`.
2. A `composition` glob.
3. A layer's glob, in the order the layers are listed.

**`vertical` and `modular`:**

1. A `composition` glob.
2. A `shared` glob.
3. A boundary's glob. When several boundaries match, the most specific glob
   wins: one that is exactly the module's path beats every wildcard, and
   otherwise the glob with the most literal segments wins (`web/billing/**`
   has two, `**/orders/**` one). If two boundaries tie, the module is in
   neither: it is ambiguous, and its dependencies are not checked.
4. In `vertical` with layers, a layer's glob places the module inside its
   boundary. A module in a boundary that matches no layer is unclassified.

**`snowflake`** matches no globs. It finds the innermost star around the
module, then:

1. A package an entry names exactly has that entry's role: contracts, an
   assembler or an engine. The star's own package is unclassified when no
   list names it.
2. Any other module is placed by its position: the shallowest directory, on
   the way down from the star to the module, that is a listed entry or holds
   a package, looking through directories that hold none. A position that
   is an entry makes the module unclassified, since it lies beneath a core
   package that no entry names; any other position is a leaf.

Packages nested in a leaf belong to it: `internal/web/fetch` is in the leaf
`internal/web`. A package nested in a core package does not: it must be
listed itself. Since `.` holds everything, a snowflake module is
unclassified only as a star's unlisted own package, a package beneath a core
package, an unread package something imports, or a misfit in a base
revision.

A module nothing places is **unclassified**, and its dependencies are not
checked.

## Rules

`rules.fail_on` and `rules.warn_on` take these names:

| Name | What it counts |
| --- | --- |
| `breaking_changes` | Contracts changed incompatibly, removed or moved, since the base given with `--base`. |
| `layer_violation` | Module dependencies that break the architecture's rules. |
| `dependency_cycles` | In `modular`, dependencies between boundaries that close a cycle. |
| `unclassified` | Modules `treaty.yaml` places nowhere, such as those no glob matches or, in `snowflake`, a star's own package that none of its lists names or a package beneath a core package, and packages imported from a directory Treaty does not read, whose dependencies are therefore not checked. |
| `ambiguous_boundaries` | Modules two or more boundaries claim equally. |
| `impure_contracts` | In `snowflake`, contracts that import more than the pure standard library or declare a package-level variable that is not a sentinel error. See [Pure contracts](#pure-contracts). |

With `--base`, only what the change introduced counts. A violation whose
module dependency the base already had is still listed, marked existing, and
a module the base already had unclassified or ambiguous is still listed, but
neither fails nor warns. So a codebase that already breaks its rules can adopt Treaty
without fixing everything first, while the debt can't grow.

`treaty init` writes every rule that applies to the architecture into
`fail_on`: `breaking_changes` alone for `none`; adding `layer_violation` and
`unclassified` for the layer architectures; adding `impure_contracts` too for
`snowflake`; adding `ambiguous_boundaries` for `vertical`, and
`dependency_cycles` too for `modular`. A snowflake's dependency violations
are `layer_violation`s and its placement is never ambiguous. Keep
`unclassified` in a snowflake's `fail_on`: there it means a star's own
package listed nowhere, a package beneath a core package, or an import of a
package Treaty does not read, whose dependencies would otherwise go
unchecked.

## Ignoring a finding

`rules.ignore` lists single findings that are expected, each with why:

```yaml
rules:
  fail_on: [breaking_changes, layer_violation]
  ignore:
    - finding: variant_mismatch
      target: go:internal/nn:block
      fingerprint: "d1b1a6491c0d"
      reason: The amd64 and arm64 kernels each use their own block size
    - finding: layer_violation
      target: go:internal/adapters/htmlmap -> go:internal/adapters/gitvcs
      fingerprint: "4b0e8d1c22f9"
      reason: Shared timestamp helper; moving it inward next quarter
```

- `finding` is the finding's kind, as reports name it: `breaking`,
  `implementers`, `moved`, `compatible`, `new_contract`, `new_module`,
  `layer_violation`, `cycle`, `unclassified`, `ambiguous_boundary`,
  `impure_contracts` or `variant_mismatch`. Implementation-only changes gather many symbols and
  cannot be ignored.
- `target` is the symbol or module the finding is about, by its id, or for a
  dependency between modules `from -> to`, ending in ` (test)` for one only
  tests make; for an impurity, the module and what it holds, such as
  `go:internal/contracts: var Registry`. Kind and target identify the
  finding however the code around it moves.
- `fingerprint` sums up what the finding said when it was ignored, from the
  data behind it, never its lines: the signatures for a breaking change, the
  declarations for a variant mismatch, the module dependency for a
  violation. When the finding changes, such as a constant's value or a
  signature changing again, the fingerprint no longer matches, and the
  finding counts again, as its kind, marked as ignored before, until it is
  ignored anew.
- `reason` is required, so a reviewer can judge it.

This is also how a `snowflake` accepts a dependency that breaks its rules for
now, sideways or on an assembler, and an impurity in its contracts. There is no other exception mechanism and
no quota: each one is an entry, with its reason.

```yaml
rules:
  fail_on: [breaking_changes, layer_violation, unclassified]
  ignore:
    - finding: layer_violation
      target: go:internal/plan -> go:internal/tracer
      fingerprint: "3f9c0a1d7b22"
      reason: The planner still calls the tracer directly; it moves behind a Tracer port in internal/contracts next
```

The fingerprint holds only the dependency, never the star, entry or leaf
names, so redrawing the stars keeps the entry unless the dependency itself
changes.

An ignored finding neither fails nor warns, in `treaty check`, in CI or on
the map, where the review queue lists it under Ignored with its reason. An
entry whose target is no longer in the code is reported, so it can be
removed. One whose target is still there but has no finding, such as a
breaking change that has since merged, stays quietly: a later change to the
same symbol is a different finding.

You rarely write entries by hand. On the live map, every finding in the
review queue has *Ignore…*, which asks for the reason and writes the entry
with the fingerprint as it stands; *Stop ignoring* removes it.

## Versions

`version` is the format of the file. It changes only when a key changes
meaning or goes away; a new optional key keeps it.

- **A file newer than your Treaty** is refused before anything else, with a
  request to upgrade.
- **A file without `version`** is version 0, from before versions. It loads
  like version 1, and `treaty check` and the `overview` tool ask you to add
  `version: 1`. The rule names from before version 1, `breaking` and
  `cycle`, are errors that give their new names.

Treaty reads the file strictly. An unknown key, adapter side or rule name is an
error that names its line and the closest known word:

```
treaty.yaml: line 2: unknown key "architecure"; did you mean "architecture"?
treaty.yaml: line 3: fail_on: unknown rule "layer_violations"; did you mean "layer_violation"?
```

A silently ignored typo is how a check stops checking.

## Editing from the map

The live map's **Terms** tab holds the whole file. Save checks it as
Treaty reads it, so a file that would not load is refused with its line
marked, and refuses to overwrite the file if it changed on disk since the
tab loaded it. The map, the checks and agents follow a saved file at once.

In `hexagonal`, `clean` and `layered`, dragging a module to another layer on
the live map edits `treaty.yaml` to place it there: it takes the module's
exact path out of every list and, unless the remaining globs already place it
there, adds it to that layer's list, keeping the file's comments. In
`vertical` and `modular`, dragging a module to another boundary adds its
exact path to that boundary's globs, which beats every wildcard; dragging it
to `shared` or `composition` adds it there. A `shared` or `composition`
wildcard that still matches it wins, since both come before boundaries, so
that move is refused with the reason. A boundary's region menu has *Rename
boundary…*, which renames its key here.

In `snowflake`, a spoke is its directory, so moves go by directory. A module
at the top of its position, a package beneath a core package, or a star's
own package can move into the contracts, the assemblers or the engines of
its own star, the innermost star holding it, or between the lists, which
adds its directory to that list under `stars` and takes it out of the
others; a star written with no value gets a mapping, a missing list is
created, and a star that still says `orchestrators` keeps that key for its
assemblers. Dropped on the band around a star's contracts, a module becomes
an assembler, or with Shift an engine; Shift on the band switches an
assembler and an engine. A core package other than its star's own can move
out again, back to its own spoke. A star's own package never leaves its
core, a package beneath a core package is never a spoke, a package nested
in a spoke moves only with it, and
putting a module in another spoke or another star's core means moving its
files: the map refuses those with the reason. A move never creates a star,
and a star has no *Rename*, since its directory names it; its menu has *Zoom
to this star*. Selecting a core package marks its entry's line under its
star's `contracts`, `assemblers`, `engines` or `orchestrators`, and a leaf its star's key line,
however the key is written, such as `internal/` or `./internal`, or a star
written on one line.

Selecting a module marks, in the Terms tab, the line of each glob that
places it: the one that won, by the order in
[How a module is placed](#how-a-module-is-placed).

Previewing another architecture on the map never touches the file; it
changes only when you choose *Use this architecture*.
