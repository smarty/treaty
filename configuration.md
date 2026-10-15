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
| `architecture` | `none`, `hexagonal`, `clean`, `layered`, `vertical` or `modular` | all | `none` | The rules dependencies must follow. See [Architectures](#architectures). |
| `composition` | list of globs | all but `none` | none | Modules that wire everything together, such as Go `main` packages. Composition may use every module; nothing may use composition. In `hexagonal`, it may also go under `layers`. |
| `layers` | map of layer name to list of globs | `hexagonal`, `clean`, `layered`, `vertical` | none | The layers and the modules in each. See [Layers](#layers). |
| `layers.adapter.driving`, `layers.adapter.driven` | list of globs | `hexagonal` | none | The two sides of the adapter layer: adapters that call into the core, such as a CLI or web handlers, and adapters the core calls, such as storage. |
| `shared` | list of globs | `vertical`, `modular` | none | Code every boundary may use. Shared code may use only shared code. |
| `boundaries` | map of boundary name to list of globs | `vertical`, `modular` | none; at least one is required | Named groups of modules that are kept apart. See [Boundaries](#boundaries). |
| `public` | list of globs | `modular` | the top of each boundary | The modules other boundaries may use. See [Boundaries](#boundaries). |
| `rules.fail_on` | list of rule names | all | `[breaking_changes, layer_violation, dependency_cycles]` | Findings that fail `treaty check`. See [Rules](#rules). |
| `rules.warn_on` | list of rule names | all | `[unclassified, ambiguous_boundaries]` | Findings that `treaty check` reports as warnings without failing. A rule in both lists fails. |

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
can't be layer names.

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

A module nothing places is **unclassified**, and its dependencies are not
checked.

## Rules

`rules.fail_on` and `rules.warn_on` take these names:

| Name | What it counts |
| --- | --- |
| `breaking_changes` | Contracts changed incompatibly, removed or moved, since the base given with `--base`. |
| `layer_violation` | Module dependencies that break the architecture's rules. |
| `dependency_cycles` | In `modular`, dependencies between boundaries that close a cycle. |
| `unclassified` | Modules no glob places, whose dependencies are therefore not checked. |
| `ambiguous_boundaries` | Modules two or more boundaries claim equally. |

With `--base`, only what the change introduced counts. A violation whose
module dependency the base already had is still listed, marked existing, and
a module the base already had unclassified or ambiguous is still listed, but
neither fails nor warns. So a codebase that already breaks its rules can adopt Treaty
without fixing everything first, while the debt can't grow.

`treaty init` writes every rule that applies to the architecture into
`fail_on`: `breaking_changes` alone for `none`; adding `layer_violation` and
`unclassified` for the layer architectures; adding `ambiguous_boundaries` for
`vertical`, and `dependency_cycles` too for `modular`.

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

Selecting a module marks, in the Terms tab, the line of each glob that
places it: the one that won, by the order in
[How a module is placed](#how-a-module-is-placed).

Previewing another architecture on the map never touches the file; it
changes only when you choose *Use this architecture*.
