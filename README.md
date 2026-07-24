# flakelens

Find flaky (not broken) CI jobs from the GitHub Actions history a repo
already has — no rerun, no test-report artifact, no config.

```
$ GITHUB_TOKEN=$(gh auth token) flakelens rust-lang/rust-analyzer
Jobs that failed in isolation (siblings in the same run passed) on 2+ distinct branches/occasions:

- proc-macro-srv: 4 isolated failures (3 distinct occurrences)
    commit 6099dec4, branch fix-postfix-err-completion, https://github.com/rust-lang/rust-analyzer/actions/runs/29816168986
    commit 3e9ed375, branch fix-postfix-err-completion, https://github.com/rust-lang/rust-analyzer/actions/runs/29814823463
    commit 756aac72, branch fix_assertion, https://github.com/rust-lang/rust-analyzer/actions/runs/29808269098
    commit e7f67080, branch feat/ungrammar-no-std-support, https://github.com/rust-lang/rust-analyzer/actions/runs/29807621521
```

## Install

```
go install github.com/nabsei/flakelens@latest
```

Or clone and build:

```
git clone https://github.com/nabsei/flakelens
cd flakelens
go build -o flakelens .
```

## Usage

```
flakelens <owner/repo> [max-runs]
```

- `max-runs` — how many completed workflow runs to scan, most recent first (default 100).
- Set `GITHUB_TOKEN` to raise the GitHub API rate limit from 60/hour to
  5000/hour — `GITHUB_TOKEN=$(gh auth token) flakelens <owner/repo>` if
  you have the `gh` CLI installed. Unauthenticated works for small repos.

## How it works

Within a single workflow run, most jobs share the same commit, so a genuine
code problem (a real compile error, a real lint violation) tends to fail
most or all jobs in that run together. A job that fails *alone*, while its
sibling jobs in the same run pass, isn't explained by "the code was broken"
— and a job that does that repeatedly, across unrelated commits, is what
flaky (non-deterministic) jobs look like.

Repeated isolated failures on the *same* PR branch are collapsed into a
single occurrence: two failures in a row while someone's mid-debugging their
own PR is presumptively the same still-broken code, not flakiness. A job
only gets reported once it's failed in isolation on **2 or more distinct
branches/occasions** — different PRs, different authors, or different
commits on the default branch.

Lint/format jobs (`rustfmt`, `clippy`, `eslint`, `prettier`, and similar —
matched by a name pattern) are excluded from this correlation entirely: they
check something orthogonal to the rest of the matrix, so failing "alone" is
expected, not suspicious — see [Known limitations](#known-limitations) below.

Full reasoning, including two mechanisms that were tried and rejected, is in
the doc comment at the top of `main.go`.

## Known limitations

- **The core signal assumes sibling jobs test overlapping concerns — it
  breaks when they don't.** "Isolated among its siblings" only implies
  something worth a look if the siblings would plausibly fail *together* on
  a real problem. A job checking something orthogonal to the rest of the
  matrix (most obviously: a lint/format check) fails or passes purely based
  on whether that PR's own diff satisfies it, independent of every other
  job — so it will *always* look "isolated," on real, unrelated, deterministic
  issues in each PR, with zero flakiness involved. Found this the hard way:
  flakelens reported rust-analyzer's `rustfmt` as a flakiness candidate and
  was (correctly) called out by a maintainer —
  [rust-lang/rust-analyzer#22904](https://github.com/rust-lang/rust-analyzer/issues/22904).
  Known lint/format job names are now excluded (see `lintJobNamePatterns` in
  `main.go`), but that's a denylist of common patterns, not a structural
  understanding of what a job actually tests — other orthogonal-but-not-lint
  jobs can still produce the same false signal.
- **Job granularity, not test granularity.** flakelens flags a *job*, not a
  test. On repos with narrow, single-purpose jobs (one build target) that's
  precise. On repos with a monolithic "run the whole suite" job, two
  unrelated PRs each breaking one unrelated test of their own can look like
  the same job repeating, when they're actually two different real bugs that
  happen to share a job name. Treat a hit on a whole-suite job as a lead to
  click through and read, not a verdict.
- **Aggregator/gate jobs are excluded by a name denylist** (`conclusion`,
  `all-green`, `ci-success`, `required-checks`, `success`), not by reading
  each job's actual `needs` graph. A repo using a differently-named gate job
  won't be recognized and may mask real signal underneath it.
- No pull request comment / GitHub Action integration yet — this is a
  read-only CLI you run and read.
- **None of this is a verdict.** Every finding above is "click the linked
  runs and read them before you believe it" — flakelens surfaces candidates
  from metadata, it doesn't read logs or understand what a job checks.

## License

Apache-2.0
