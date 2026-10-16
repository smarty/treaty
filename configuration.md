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
| `composition` | list of globs | all but `none` | none | Modules that wire everything together, such as Go `main` packages. Composition may use every module; nothing may use composition. In `hexagonal`, it may also go under `layers`. |
| `layers` | map of layer name to list of globs | `hexagonal`, `clean`, `layered`, `vertical` | none | The layers and the modules in each. See [Layers](#layers). |
| `layers.adapter.driving`, `layers.adapter.driven` | list of globs | `hexagonal` | none | The two sides of the adapter layer: adapters that call into the core, such as a CLI or web handlers, and adapters the core calls, such as storage. |
| `shared` | list of globs | `vertical`, `modular` | none | Code every boundary may use. Shared code may use only shared code. |
| `boundaries` | map of boundary name to list of globs | `vertical`, `modular` | none; at least one is required | Named groups of modules that are kept apart. See [Boundaries](#boundaries). |
| `public` | list of globs | `modular` | the top of each boundary | The modules other boundaries may use. See [Boundaries](#boundaries). |
| `stars` | map of star directory to list of core globs | `snowflake` | none; at least one is required | Each star's directory, and the globs of its core. Spokes are never listed: they are the star's other child directories. See [Stars](#stars). |
| `rules.fail_on` | list of rule names | all | `[breaking_changes, layer_violation, dependency_cycles]` | Findings that fail `treaty check`. See [Rules](#rules). |
| `rules.warn_on` | list of rule names | all | `[unclassified, ambiguous_boundaries]` | Findings that `treaty check` reports as warnings without failing. A rule in both lists fails. |
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
| `snowflake` | The code is a star of stars. A spoke may use itself, its own star's core and the cores of every star around it; a core may use itself and the cores around it, never its own spokes. No spoke may use a sibling spoke, or anything in a sibling branch, not even its core. | `composition`, `stars` |

In every architecture but `none`, composition may use everything and nothing
may use composition.

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
can't be layer names. The snowflake's `core` and `spoke` are not reserved, so a
`layered` file may still name a layer `core`.

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
directory, and has exactly one **core**: its contracts (interfaces, value
types and their pure methods, errors, constants) and its orchestrators.
Every other child directory of the star is a **spoke**. A spoke is a
**leaf**, or a **branch** when it is a star of its own, with its own core
and spokes.

```yaml
version: 1
architecture: snowflake
composition: ["cmd/**", "internal/factory/**"]
stars: # each star's directory -> the globs of its core
  internal:        ["internal/core/**"]
  internal/blocks: ["internal/blocks/core/**"]
  internal/gate:   ["internal/gate/core/**"]
  internal/billing: []      # core is just the internal/billing package
rules:
  fail_on: [breaking_changes, layer_violation, unclassified]
```

Here `internal` is the root star. Its core is the `internal` package and
everything under `internal/core`. `internal/blocks`, `internal/gate` and
`internal/billing` are branches, and every other directory under `internal`
that holds code, such as `internal/web`, is a leaf. Inside `internal/blocks`,
`internal/blocks/core/**` is the core and each other child, such as
`internal/blocks/www`, is a leaf of that star.

Dependencies point only inward, at cores:

- **A spoke** may use itself, every package nested in it included, its own
  star's core and the core of every star around it.
- **A core** may use itself and the cores of the stars around it, never its
  own spokes, at any depth.
- **No sideways dependencies.** No spoke may use a sibling spoke, a sibling
  branch, not even that branch's core, or anything inside another star's
  spokes. Wiring spokes together is the orchestrator's job: it declares a
  port, an interface, in its core; a spoke implements it; composition builds
  the spoke and hands it in.
- **Composition** may use everything, and nothing may use composition.

The rules:

- **A star is a directory**, relative to the repository root, never a glob:
  `.`, `internal`, `internal/blocks`.
- **The star's own package is always in its core**, since that is where its
  orchestrators live. Only an exact `composition` entry takes it out, making
  it a local factory that nothing may use.
- **A core glob lies inside its star** and names a child directory of it:
  `internal/core/**` is fine, but `internal/**`, `internal/*` and
  `internal/*/contracts/**` are refused, since they would make every spoke
  core. An exact path may name any module inside the star. `[]` leaves the
  core as just the star's own package.
- **Spokes are found by directory.** A new directory under a star is a leaf
  the moment it holds code, so it is checked at once and cannot hide a
  sideways dependency by being left out of the file. Turning a leaf into a
  branch is one line, such as `internal/plan: []`.
- **One root star.** Every other star is a direct child directory of a star.
  Code in both `internal/` and `pkg/` needs the root `.` with both as
  branches.
- **A branch's core is its own.** No core glob of a star may start inside one
  of its branches.

Treaty checks imports between modules, not what a core declares, so keeping
a core to contracts and orchestrators is up to you; its abstractness metric
shows how it is going. A port that a leaf of one star implements and a leaf
of another uses lives in the core of the nearest star around both, so the
root's core tends to grow.

`layers`, `shared`, `boundaries` and `public` are errors in `snowflake`, and
`stars` is an error in every other architecture:

```
treaty.yaml: line 4: unknown key "starz"; did you mean "stars"?
treaty.yaml: invalid architecture: layers, shared, boundaries and public do not apply to the snowflake architecture; a star's core is what its spokes share
treaty.yaml: invalid architecture: star "internal/*" must be a directory path relative to the repository root, not a glob
treaty.yaml: invalid architecture: core glob "internal/**" of star internal does not name a directory of its own; start it with a child directory of internal, such as internal/core/**
```

The snowflake builds on the Presentation-Abstraction-Control pattern
(Coutaz, 1987), whose agents form a hierarchy and talk only through their
parent; the Mediator pattern (Gamma et al., 1994), whose colleagues talk only
through the mediator; and hexagonal ports and adapters (Cockburn, 2005),
whose ports are declared inward and whose implementations are handed in.
What it adds is the recursion as a static import rule, checked on every
module dependency, and zero sideways dependencies with no shared layer
between siblings.

## Globs

Every glob is matched against a module's path, relative to the repository
root. A module is a directory of source: a Go package, a directory of
JavaScript or TypeScript files, or a Python package. The repository root's
path is `.`.

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

**`snowflake`:**

1. A `composition` entry that is exactly the module's path.
2. A core entry that is exactly the module's path: the core of that star.
3. A `composition` glob.
4. The innermost star holding the module. None, and the module is outside
   the root star: unclassified.
5. The star's own directory: its core.
6. A glob of that star's core. Only the innermost star's globs count, so an
   outer core never reaches into a branch.
7. Otherwise, the spoke named by the star's child directory holding the
   module. Every package nested in a leaf belongs to that leaf:
   `internal/web/fetch` is in spoke `internal/web`.

A module nothing places is **unclassified**, and its dependencies are not
checked.

## Rules

`rules.fail_on` and `rules.warn_on` take these names:

| Name | What it counts |
| --- | --- |
| `breaking_changes` | Contracts changed incompatibly, removed or moved, since the base given with `--base`. |
| `layer_violation` | Module dependencies that break the architecture's rules. |
| `dependency_cycles` | In `modular`, dependencies between boundaries that close a cycle. |
| `unclassified` | Modules `treaty.yaml` places nowhere, such as those no glob matches or, in `snowflake`, those outside the root star, whose dependencies are therefore not checked. |
| `ambiguous_boundaries` | Modules two or more boundaries claim equally. |

With `--base`, only what the change introduced counts. A violation whose
module dependency the base already had is still listed, marked existing, and
a module the base already had unclassified or ambiguous is still listed, but
neither fails nor warns. So a codebase that already breaks its rules can adopt Treaty
without fixing everything first, while the debt can't grow.

`treaty init` writes every rule that applies to the architecture into
`fail_on`: `breaking_changes` alone for `none`; adding `layer_violation` and
`unclassified` for the layer architectures and `snowflake`; adding
`ambiguous_boundaries` for `vertical`, and `dependency_cycles` too for
`modular`. A snowflake needs no rule of its own: its violations are
`layer_violation`s, it has no cycles, since every allowed dependency between
regions points up the star tree, and placement is never ambiguous.

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
  `layer_violation`, `cycle`, `unclassified`, `ambiguous_boundary` or
  `variant_mismatch`. Implementation-only changes gather many symbols and
  cannot be ignored.
- `target` is the symbol or module the finding is about, by its id, or for a
  dependency between modules `from -> to`. Kind and target identify the
  finding however the code around it moves.
- `fingerprint` sums up what the finding said when it was ignored, from the
  data behind it, never its lines: the signatures for a breaking change, the
  declarations for a variant mismatch, the module dependency for a
  violation. When the finding changes, such as a constant's value or a
  signature changing again, the fingerprint no longer matches, and the
  finding counts again, as its kind, marked as ignored before, until it is
  ignored anew.
- `reason` is required, so a reviewer can judge it.

This is also how a `snowflake` accepts a sideways dependency for now. There
is no other exception mechanism and no quota: each one is an entry, with its
reason.

```yaml
rules:
  fail_on: [breaking_changes, layer_violation, unclassified]
  ignore:
    - finding: layer_violation
      target: go:internal/plan -> go:internal/tracer
      fingerprint: "3f9c0a1d7b22"
      reason: The plan executor still calls the tracer directly; it moves behind a Tracer port in internal/core next
```

The fingerprint holds only the dependency, never the star or spoke names, so
moving a star's boundary keeps the entry unless the dependency itself
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

In `snowflake`, a spoke is its directory, so a module can move only into the
core of a star that holds it, which adds its exact path to that star's list
under `stars`; out of a core it is in by its exact path, back to its own
directory's spoke; or to `composition`. Putting a module in another spoke
means moving its files, and the map says so. A move never creates a star,
and a star has no *Rename*, since its directory names it; its menu has
*Zoom to this star*. A module a star places by its directory, its own
package or a spoke, marks that star's key line under `stars`.

Selecting a module marks, in the Terms tab, the line of each glob that
places it: the one that won, by the order in
[How a module is placed](#how-a-module-is-placed).

Previewing another architecture on the map never touches the file; it
changes only when you choose *Use this architecture*.
