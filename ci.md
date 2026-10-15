# Treaty in CI

`treaty ci` checks a pull request and shows the result on the pull request:

- It compares the branch with its **merge base** with the target branch, so
  every commit on the branch counts and nothing the target gained since does.
- Each finding that **fails** the check, as `rules.fail_on` says, or **warns**,
  as `rules.warn_on` says, is annotated on its line.
- The full report goes where the pull request's reviewers read it: the job
  summary on GitHub, or a Code Insights report on Bitbucket.
- It writes the **map of the change** to `.treaty/out/map.html`, for the build
  to keep. Open it in a browser to see the review queue, the map and the
  inspector without a server.
- It exits 1 when the check fails, as `treaty check` does.

As in `treaty check` with a base, only what the branch brings fails: layer
violations, unclassified modules and the like that the target branch
already had are listed as existing and fail nothing. See
[Rules](configuration.md#rules) for what each rule counts.

## GitHub Actions

```yaml
name: Treaty
on: pull_request

jobs:
  treaty:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0 # the merge base needs the history
      - uses: smarty/treaty@v0.3.0
```

The action installs the release it is pinned to, checks it against the
release's checksums, runs `treaty ci`, keeps the map as the `treaty-map`
artifact, and fails the job when the check fails. Its inputs:

| Input | Default | Does |
| --- | --- | --- |
| `version` | The release the action is pinned to | Another release to run, such as `v0.3.0`; `source` builds the action's own source with Go |
| `target` | The pull request's base branch | The branch the pull request merges into |
| `working-directory` | `.` | The directory holding `treaty.yaml`, for a repository that keeps it below the root |
| `map` | `true` | Keep the map as the `treaty-map` artifact |

Its output `result` is `pass` or `fail`.

GitHub checks out a pull request merged into its base branch, so the check
sees what the base branch would get. GitHub shows at most ten error and ten
warning annotations per step; the job summary lists every finding that fails
or warns.

## Bitbucket Pipelines

```yaml
image: golang:1.27 # any image with curl, git and tar

clone:
  depth: full # the merge base needs the history

pipelines:
  pull-requests:
    "**":
      - step:
          name: Treaty
          script:
            - git fetch origin "+refs/heads/$BITBUCKET_PR_DESTINATION_BRANCH:refs/remotes/origin/$BITBUCKET_PR_DESTINATION_BRANCH"
            - curl -fsSL https://raw.githubusercontent.com/smarty/treaty/v0.3.0/scripts/install.sh | sh -s v0.3.0 /usr/local/bin
            - treaty ci
          artifacts:
            - .treaty/out/map.html
```

A pipeline clones only the branch it builds, so the first line fetches the
destination branch. `treaty ci` then publishes a Code Insights report named
Treaty on the built commit, with each finding that fails or warns
annotated, and the pull request shows it under Reports and on the changed
lines. Inside a pipeline, Bitbucket's own proxy authorizes the report, so no
token or app password is needed. A new build of the same commit replaces the
report.

## Anywhere else

`treaty ci` works in any CI, and on your own machine, where it publishes
nothing and prints the summary:

```sh
curl -fsSL https://raw.githubusercontent.com/smarty/treaty/v0.3.0/scripts/install.sh | sh -s v0.3.0 /usr/local/bin
treaty ci --target main
```

Without `--target`, and outside a pull request build, it compares with the
default branch: `origin/HEAD`, else `origin/main`, `origin/master`, `main` or
`master`. A branch name is read as the remote's branch when the clone has it,
so `--target main` compares with `origin/main`.

For other formats, `treaty check --merge-base main` runs the same check and
prints the report as JSON, or with `--format markdown` as the summary GitHub
shows; `treaty map --merge-base main` draws the same map.

## When it cannot compare

- **The target branch is not in this clone**: a clone of one branch lacks
  the others. Fetch it, as the Bitbucket example does:
  `git fetch origin main:refs/remotes/origin/main`.
- **No merge base**: a shallow clone stops before the branch left the
  target. Clone the whole history (`fetch-depth: 0`, or `clone: depth: full`),
  or run `git fetch --unshallow`.
