# Contract scoring

How Treaty can measure how much of a repository's contract surface is behaviorally proven: not only executed by tests, but checked by them, in a way that would fail if the contract stopped holding.

Status: proposal, October 3, 2026. Examples and error congruence, Treaty's own coverage instrumentation, and reach, decisions and boundaries were built on October 6, 2026, for Go; see [Built](#built). The rest is not built yet. Contract-scoped mutation testing was deferred on September 29, 2026, and this document says where it would fit when it returns.

## The problem

Proving a contract by enumerating its input domain works for a well-bounded function. `TruncateString32(value string) string` has the domain of all strings, and its edges are the empty string, one character, 32 characters and 33 characters. That does not scale:

- Many functions have no well-defined domain, or one too large to partition by hand.
- Many contract elements are not functions at all: interfaces, types, constructors, error values, stateful protocols.
- Line coverage measures execution, not verification. It can read 100% while behavior at the contract goes untested.

So Treaty measures **claims** instead of domains. A contract is proven when every claim it makes has a test that goes through the contract and would fail if the claim became false.

## Two questions

A contract is behaviorally proven only when both of these score well. Neither replaces the other.

1. **Sensitivity: do the tests check the behavior the code has?** Mutation testing answers this best, and it applies to every kind of contract element. Treaty approximates it cheaply until full mutation testing returns.
2. **Claim coverage: does the behavior match what the contract states?** Mutation testing cannot answer this, because it measures tests against the code as written:
   - Missing code gives it nothing to mutate. If `TruncateString32` slices bytes and splits a multi-byte character, no mutant exposes the bug, because there is no rune handling to mutate. Missing handling, validation and error paths are a large share of real bugs.
   - If the code is wrong and the tests encode the wrong behavior, every mutant dies.
   - A high score can mean brittle tests that pin incidental details such as log text or map order.
   - Equivalent mutants keep the score from cleanly reaching 100%, and the usual mutation operators say nothing about concurrency, timing or resource cleanup.
   - An interface has no code to mutate. It is proven only through its implementations.

   Boundaries taken from the code's own comparisons share the first blind spot: they find only the edges the code already knows about.

## Claims must be structured, not prose

Reading claims from prose needs either a generative model, which is not deterministic, or a specification language, which is a large undertaking and asks too much discipline of developers. Treaty does neither. It counts only claims that are already structured, in forms the compiler or simple conventions provide:

| Claim source | What it states | How Treaty reads it |
| --- | --- | --- |
| Types and signatures | The shape of inputs and outputs | The compiler proves these; nothing to measure |
| Exported sentinel errors and error types reachable from a symbol | "This can fail with `ErrX`" | The call graph; proven by a test through the contract that asserts `errors.Is(err, ErrX)` or `errors.As` |
| Exported constants used in comparisons, such as `MaxLength = 32` | A published boundary | An exported constant is part of the contract surface, so it is a stated boundary, not one inferred from the implementation |
| Domain types with validating constructors, such as `type Name32 string` | The domain itself | The constructor holds the boundaries, so they are proven once there, not in every function that takes the type |
| Interfaces | Implementations can be swapped for each other | A test helper that takes the interface, and the implementations it runs against |
| Examples: `Example` functions and unit tests that exercise the contract | Concrete input and output | See [Examples](#examples) |
| `Fuzz` functions | A property holds across inputs | Already run by the Tests tab |
| The doc comment's `Errors:` section | The errors a function promises | Only the identifier on each `- ErrX:` line, which a regular expression extracts; the description is never interpreted |

None of this needs a new language. It rewards design worth having anyway:

- **Sentinel errors**, or typed errors checked with `errors.As`, make each failure a named claim. An error built only with `fmt.Errorf` and no sentinel behind it is a failure Treaty cannot measure.
- **Domain types instead of primitives** turn a vague domain into a type. Many "ill-defined domain" functions stop being ill-defined, because the type narrows what they accept.
- **Tiny interfaces** keep port claims small enough that one shared conformance suite covers them fully.
- **Exported constants for boundaries** publish the edges instead of hiding them as literals.
- **Examples and `Fuzz` functions** state concrete and general behavior in forms `go test` already checks.

A contract that scores poorly is usually also one that is hard to use correctly.

### Where generative AI fits

Generative AI is kept out of the measurement and used only to write tests. When a doc comment says "truncates to 32 runes" and nothing structured backs it, an agent working through Treaty's MCP tools can read it and write an example or a `Fuzz` function. Once that exists, the claim is structured and measuring it stays deterministic. The authoring step can vary between runs; the score cannot.

### What stays unmeasured

Intended behavior nobody wrote down anywhere cannot be measured by any tool, AI or not. Treaty says so plainly: a contract with no structured claims beyond its signature shows as **no stated claims**, never as proven.

## Prior art

Most of the parts exist as named measures. What does not exist is putting them together per contract, counting only tests that go through the contract, and checking stated claims against reachable behavior in both directions. The references were checked on October 3, 2026; links are under [Sources](#sources).

| Measure | What it is | Tooling or source | Where it fits here |
| --- | --- | --- | --- |
| Mutation score | Killed mutants / (all mutants − equivalent ones) | Go: gremlins (active, still 0.x); go-mutesting (the original repository looks inactive). Java: PIT. JS/TS: StrykerJS. Python: mutmut | Sensitivity, the full version |
| Test strength | Killed / (killed + survived), leaving out mutants no test covers | PIT, since 1.6.1 | Sensitivity measured only over reached code, which is how Treaty's measures separate reach from proof |
| Pseudo-tested methods (extreme mutation) | A method that tests cover, yet no test fails when its body is removed or replaced with a default return | Niedermayr, Juergens and Wagner, *Will My Tests Tell Me If I Break This Code?*, CSED '16, which found 6–53% of covered methods pseudo-tested across 14 Java projects. Descartes, a PIT engine: Vera-Pérez et al., ASE 2018. Vera-Pérez, Danglot, Monperrus and Baudry, *A Comprehensive Study of Pseudo-tested Methods*, Empirical Software Engineering 24(3), 2019 | A cheap per-contract version of mutation testing: one mutant per exported function |
| Checked coverage | The share of executed statements that affect an assertion, found by dynamic slicing backward from the assertions | Schuler and Zeller, *Assessing Oracle Quality with Checked Coverage*, ICST 2011, which found it more sensitive to weak oracles than mutation testing; extended in *Checked coverage: an indicator for oracle quality*, STVR 2013 | The Asserted level |
| Diff-scoped mutation in review | Mutants only on changed lines, shown in code review as individual findings, not a score | Petrović and Ivanković, *State of Mutation Testing at Google*, ICSE-SEIP 2018; used by 6,000 engineers on all code changes they author or review | Precedent for contract-scoped mutation and for showing gaps instead of a number |
| MC/DC and decision coverage | MC/DC: each condition in a decision shown to affect its outcome on its own | DO-178C requires statement coverage at levels A to C, decision coverage at A and B, and MC/DC at level A only. Go's cover tool measures statement blocks only, not branches, which is one reason Treaty instruments code itself | The Edged level |
| Requirements-based test coverage | A matrix tracing requirements to tests | DO-178C, ISO 26262; usually maintained by hand | Claim coverage, derived here from code structure instead of a hand-kept matrix |
| API operation coverage | The operations, parameters and status codes a specification declares, compared with those the tests exercise, plus responses it does not declare | swagger-coverage, for OpenAPI 2 and 3 | Claim coverage for service contracts, including both directions: declared but untested, and returned but undeclared |
| Consumer-driven contracts | Each consumer records the interactions it relies on, and the provider is verified against them | Pact. It verifies the recorded interactions but does not measure coverage | Claims stated as examples, by the code that depends on them |
| Conformance suites | One suite run against any implementation | `testing/fstest.TestFS(fsys, expected...)` for any `fs.FS`; `golang.org/x/net/nettest.TestConn(t, makePipe)` for any `net.Conn` | Port conformance; the idiom exists, but nothing measures it |
| Structure linters | Keep errors structured | `err113` flags errors created on the spot instead of wrapping a package-level sentinel, and `==` on errors. `errorlint` flags comparisons and type assertions that break with wrapped errors. `errname` enforces the `Err` prefix for sentinels and the `Error` suffix for error types | Keep claims structured so they stay measurable |

## Levels

Each contract has a level, the highest whose conditions it meets:

0. **Unseen**: no test reaches it.
1. **Reached**: tests that enter through this contract execute its code. Coverage reached only from internal tests does not count.
2. **Asserted**: the result reaches an assertion. A test that calls the contract and discards the result stops at level 1.
3. **Edged**: every published boundary is exercised below, at and above its constant, every decision outcome is reached, and every stated error outcome is asserted.
4. **Mutation-proven**: mutating the code behind the contract makes a test that goes through the contract fail.

Levels 1 to 3 are cheap stand-ins for level 4 and show where mutation runs would pay off once they return.

## Coverage instrumentation

Treaty instruments code itself rather than relying on each language's coverage tool. Go's cover tool counts statement blocks, not branches or conditions, and reports one total per run with no record of which test reached what. The scorecard needs more than that, and Treaty's own instrumentation also works the same way in every language it reads, as its hand-written scanners do.

**What it records:**

- **Blocks**, as today, so the coverage ring keeps working.
- **Every outcome of every decision**, each `if`, loop condition and `switch` case, true and false separately.
- **Every operand of `&&` and `||`**, so a compound condition shows which operand decided it. This is what MC/DC needs.
- **Boundary comparisons**: for each comparison against an exported constant, whether a run saw the compared value below, at or above the constant.
- **Which test reached each counter.** Every counter is attributed to the test, subtest or example running at the time, so the contract-scoped measures come from one run of the suite rather than one run per contract.

**How:**

- Treaty rewrites copies of the source files with counters inserted and builds them in place of the originals, with `go build -overlay` for Go, so the working tree is never touched and line numbers are kept.
- Test files are rewritten too, so each test records when it starts and ends.
- Tests that call `t.Parallel()` run together, which blurs attribution. Treaty marks counters it cannot attribute to a single test as shared, and can run with parallelism off when a contract's measures need exact attribution.
- Counters are written to a file per test binary, and Treaty reads them after the run, as it reads cover profiles today.

The Tests tab's `go test -coverprofile` runs are replaced by this instrumentation, and block coverage is drawn as the ring exactly as before. Like the Tests tab, it starts with Go. JavaScript, TypeScript and Python follow with the same counters once Treaty runs their tests.

## Measures

Every measure is computed per contract element. **A test exercises a contract** when it calls the contract's symbol, or one of its members, either directly or through helpers in its own test package, transitively. A test named for a symbol, such as `TestService_Map`, also targets it. These are the rules the Tests tab already uses to show the tests that use a selection.

Every measure is a fraction with an explicit denominator, or a list. They are never blended into one grade, because a blend hides exactly the gap worth seeing.

| Measure | Value | Cost | Deterministic |
| --- | --- | --- | --- |
| Reach | Blocks reachable from the contract that tests through the contract cover / all blocks reachable from it | Cheap: one instrumented run, with counters attributed per test | Yes, given tests that are not flaky |
| Examples | Count of examples that exercise the contract; see [Examples](#examples) | Static | Yes |
| Not pseudo-tested | Pass or fail, per exported function or method | Cheap: one mutant, run against only the tests that exercise the contract | Yes |
| Error congruence | Asserted / (declared ∪ reachable), plus the gap lists | Static: call graph, doc comments and test code | Yes |
| Boundaries | Published-constant comparisons seen below, at and above the constant / all such comparisons | Cheap once instrumented: read from the boundary counters | Yes |
| Decisions | Decision outcomes and `&&`/`||` operands reached by tests through the contract / all of them reachable from it | Cheap once instrumented | Yes |
| Port conformance | Implementations run through a shared suite / implementations; dropped, see [Build order](#build-order) | Static | Yes |
| Mutation score | Killed / non-equivalent mutants, counting only tests through the contract | Expensive; deferred | Yes, with fixed operators and seeds |

### Examples

An example is anything that shows the contract's behavior on concrete input:

- an `Example` function with an `// Output:` comment, which `go test` checks;
- a unit test that exercises the contract, by the rule above.

Both count the same. The measure is the **number of examples that exercise each contract**, listed by name so the inspector can open each one. A contract with no examples shows as **no examples**. Once assertion detection exists, the count splits into examples that assert on the contract's result and those that only call it, since only the first kind shows behavior.

For a type, its methods' examples count toward it, as its coverage includes its methods. For an interface, the examples of its implementations count only when they go through the interface.

### Error congruence

Three sets per contract:

- **Declared**: the identifiers in the doc comment's `Errors:` section.
- **Reachable**: the sentinel errors and error types the call graph can return from the contract.
- **Asserted**: those that a test through the contract checks with `errors.Is` or `errors.As`.

The gaps are the findings:

| Gap | Meaning | Fix |
| --- | --- | --- |
| Reachable, not declared | Behavior the contract does not state | Document the error, or stop returning it |
| Declared, not reachable | A stated claim the code cannot meet | Fix the code or the comment |
| Declared or reachable, not asserted | A claim no test proves | Write the test |

The first two gaps are checks between the contract and the code, which coverage tools cannot make because they do not know what the contract states.

### Boundaries

A boundary is an exported constant compared against in code reachable from the contract, such as `len(value) > MaxLength`. It is proven when tests through the contract compare a value below the constant, one equal to it, and one above it. For `TruncateString32` with `MaxLength = 32`, that is 31, 32 and 33 characters: the edges from the opening example, found without enumerating the domain. An equality comparison needs only equal and not equal.

The instrumentation records each comparison's operands, so this holds inside compound conditions such as `a && len(value) > MaxLength` too, as long as the comparison was evaluated.

Boundaries from literals are not counted: a literal is not a published claim, and counting it would only measure the implementation against itself.

### Port conformance

For each interface, a matrix of implementations by the shared suites run against them. A shared suite is a test helper that takes the interface as a parameter. An implementation no suite runs against is a gap.

## Reporting

- **Per contract**: the level and every measure, in the inspector beside the coverage ring.
- **Per module or group**: the spread across its contracts, such as "12 pseudo-tested, 3 with undeclared errors, 5 with no examples", not an average.
- **Gaps over scores**: following Google's experience, the map shows individual gaps you can act on, such as a surviving pseudo-test, an unasserted `ErrNotFound`, or an adapter missing from a conformance suite, rather than leading with a percentage.
- **Over MCP**: an agent can ask for a contract's gaps before editing it and close them by writing tests, which keeps the measurement deterministic.

## Build order

1. **Examples**, static and cheap: it reuses the Tests tab's filter of tests by symbol. Built.
2. **Error congruence**, purely structural, and it pays off sentinel-error discipline right away. Built.
3. **Treaty's own instrumentation** for Go, replacing `go test -coverprofile`, with counters attributed per test. Reach, decisions and boundaries depend on it. Built.
4. **Contract-scoped reach**, then **decisions** and **boundaries**, all read from the same counters. Built.
5. **Pseudo-tested detection**, the cheapest real evidence of sensitivity, and it maps to contracts one to one.
6. **Port conformance**. Dropped on October 6, 2026: it assumes an architecture built on ports, and Treaty serves five.
7. **Assertion detection**, which splits examples into asserting and calling. The instrumentation can later record the values that reach assertions, which is the start of checked coverage.
8. **Contract-scoped mutation testing**, aimed at the contracts the cheaper measures flag.

## Built

Examples and error congruence are built for Go, and show as rows of a module's metrics table, before and after, and as a contract's Examples list and Errors table in the inspector. How the build reads this document:

- **Examples** are `Test` functions and `Example` functions with an `// Output:` comment that use the contract, by the Tests tab's rules. `Fuzz` functions do not count. A type counts its methods' tests. The module rows are contracts with examples, of its top-level contracts, and the number of distinct examples.
- **Declared** errors are the identifiers on the `- Name:` lines of a function's or method's `Errors:` section. `pkg.Name` resolves to the repository module of that package name, or is external, such as `io.EOF`, and not checked.
- **Reachable** errors are sentinels, named `Err…` or `err…`, that the code names other than to compare them (`==`, `!=`, `case`, `errors.Is`, `errors.As`), and error types, named `…Error`, it builds as literals. Calls within the contract's module are followed. A call into another module takes the callee's `Errors:` section when it has one, and is followed when it does not.
- **Asserted** errors are those a test that uses the contract names. It cannot tell `errors.Is(err, ErrX)` from any other mention.
- The module rows are errors proven (asserted, of each contract's declared and reachable errors), undeclared errors and errors declared but not returned. JavaScript, TypeScript and Python modules show `—` until Treaty finds their tests.

**Handled errors.** A call whose error the caller handles and drops adds nothing to what it returns. A call passes its results on only when it is part of a `return`, or when its last result, the error by convention, is assigned to a name that a later `return` in that name's scope mentions: the `if`, `for` or `switch` that declares it, or else the rest of the block. So `if slice, err := buildSlice(...); err == nil { ... }` drops `buildSlice`'s errors, and `err := f(); if err != nil { return fmt.Errorf("...: %w", err) }` passes them on. A function named without being called, such as one passed as a value, passes nothing on, and nothing inside `errors.Is` or `errors.As` does. On this repository that took `internal/app`'s undeclared errors from 69 to 37 without losing any declared error that was found before. A `return` inside a function literal still counts as its caller's.

**Instrumentation.** Treaty rewrites copies of every non-test file of the Go module being tested and builds them in with `go test -overlay`, `-vet=off` and `-count=1`; the runtime they report to, `treatycover`, exists only in the overlay. Every edit stays on its line. It counts:

- every block: a function body, the blocks of `if`, `else`, `for`, bare blocks and each `case` or `default` clause;
- both outcomes of every `if` and `for` condition, and of each operand of `&&` and `||` at its top level;
- for a comparison in a condition with an exported constant, whether the value was below, at or above it, by `cmp.Compare`; bool constants are left out.

Test files get one statement at the top of each `Test` and `Fuzz` function, `t.Cleanup(...)`, so the test's span includes its subtests, and a `defer` in each `Example`. When a test ends, the counters that rose since it started are its hits. A test that ran while another did is marked shared. A run whose instrumented build fails runs again plainly with `-coverprofile`, measuring lines only. The whole of this repository instruments, builds and runs this way: 62 files, about 10,000 probes.

**Reach, decisions and boundaries** show under Tests run in a module's metrics and a contract's inspector, in the live map, with Before empty since runs measure the working tree. A contract's code is its body or its methods' bodies and every function they call within the module; only tests that use the contract count. For a module, only tests that use one of its top-level contracts count. Reach is blocks run of all blocks, decisions are outcomes reached of all outcomes, and boundaries are comparisons seen below, at and above of all comparisons with exported constants (at and one side for `==` and `!=`).

## Sources

Checked October 3, 2026.

- Niedermayr, Juergens and Wagner, *Will My Tests Tell Me If I Break This Code?*, CSED '16: [arXiv:1611.07163](https://arxiv.org/abs/1611.07163)
- Vera-Pérez et al., *Descartes: A PITest Engine to Detect Pseudo-Tested Methods*, ASE 2018: [doi:10.1145/3238147.3240474](https://dl.acm.org/doi/10.1145/3238147.3240474); [github.com/STAMP-project/pitest-descartes](https://github.com/STAMP-project/pitest-descartes)
- Vera-Pérez, Danglot, Monperrus and Baudry, *A Comprehensive Study of Pseudo-tested Methods*, EMSE 2019: [arXiv:1807.05030](https://arxiv.org/abs/1807.05030)
- Schuler and Zeller, *Assessing Oracle Quality with Checked Coverage*, ICST 2011: [doi:10.1109/ICST.2011.32](https://dl.acm.org/doi/10.1109/ICST.2011.32); STVR 2013 extension: [doi:10.1002/stvr.1497](https://onlinelibrary.wiley.com/doi/10.1002/stvr.1497)
- Petrović and Ivanković, *State of Mutation Testing at Google*, ICSE-SEIP 2018: [doi:10.1145/3183519.3183521](https://dl.acm.org/doi/10.1145/3183519.3183521)
- DO-178C structural coverage by software level: [LDRA, DO-178C and structural coverage analysis](https://ldra.com/ldra-blog/do-178c-structural-coverage-analysis/)
- Go's cover tool and block counting: [go.dev/blog/cover](https://go.dev/blog/cover)
- Mutation tools: [gremlins](https://github.com/go-gremlins/gremlins), [go-mutesting](https://github.com/zimmski/go-mutesting), [PIT](https://github.com/hcoles/pitest) and its [test strength statistic](https://pitest.org/quickstart/maven/), [StrykerJS](https://github.com/stryker-mutator/stryker-js), [mutmut](https://github.com/boxed/mutmut)
- API contracts: [swagger-coverage](https://github.com/viclovsky/swagger-coverage), [Pact specification](https://github.com/pact-foundation/pact-specification)
- Conformance suites: [testing/fstest](https://pkg.go.dev/testing/fstest), [golang.org/x/net/nettest](https://pkg.go.dev/golang.org/x/net/nettest)
- Linters: [err113](https://github.com/Djarvur/go-err113), [errorlint](https://github.com/polyfloyd/go-errorlint), [errname](https://github.com/Antonboom/errname)
