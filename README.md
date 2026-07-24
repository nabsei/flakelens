# flakelens

Find flaky (not broken) CI jobs from the GitHub Actions history a repo
already has — no rerun, no test-report artifact, no config.

```
$ GITHUB_TOKEN=$(gh auth token) flakelens denoland/deno
Jobs that failed in isolation (siblings in the same run passed) on 2+ distinct branches/occasions:

- test specs (1/2) debug linux-aarch64: 2 isolated failures (2 distinct occurrences) [CONFIRMED: specs::upgrade::stable]
    commit 562ff5c8, branch main, https://github.com/denoland/deno/actions/runs/30050862308 — specs::upgrade::stable
    commit 39c22a20, branch main, https://github.com/denoland/deno/actions/runs/30036918944 — specs::upgrade::stable
```

Both runs above are unrelated merges to `main` (one an HTTP stream fix, one
an FFI fix) — yet both fail on the exact same test, `specs::upgrade::stable`.
flakelens downloaded both logs and extracted that test name itself — see
[How it works](#how-it-works). Two unrelated PRs, same test breaking in
isolation both times: that's the signal this tool is built to find.

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

### Log verification

A job name isn't a test — it can be a single build step or an entire suite.
For every job that clears the isolated-failure threshold above, flakelens
downloads each occurrence's log and tries to extract the actual test that
failed (pytest, `cargo test`, and `go test` output formats — a handful of
regexes, not a real parser per framework). The result is tagged:

- **`CONFIRMED: <test>`** — every occurrence where a test name could be read
  failed on the *same* test. Strong signal.
- **`REJECTED: different tests failed (...)`** — the occurrences disagree.
  This is what a monolithic "run everything" job repeating under this
  correlation usually means: two unrelated PRs each broke one unrelated test
  of their own, and they happened to share a job name — not flakiness. Real
  example, `pola-rs/polars`' `coverage-python` job:
  ```
  - coverage-python: 4 isolated failures (4 distinct occurrences) [REJECTED: different tests failed (
      tests/unit/datatypes/test_struct.py::test_struct_with_fields_non_deterministic_24568 vs
      tests/unit/io/test_iceberg.py::test_scan_iceberg_parquet_prefilter_with_column_mapping vs
      tests/unit/operations/namespaces/temporal/test_to_datetime.py::test_to_datetime)]
  ```
- **`UNVERIFIED`** — no occurrence's log matched a known format. The
  job-level correlation still stands, it's just not confirmed against logs.

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
- **Job granularity vs. test granularity — mitigated, not solved.** The [log
  verification](#log-verification) above catches the case where a
  monolithic-suite job's repeated "isolated failure" is really two unrelated
  real bugs sharing a job name (`REJECTED`), and confirms when it's really
  the same test breaking (`CONFIRMED`). But it's regex pattern-matching
  against a handful of frameworks, not a real parser: a framework it doesn't
  recognize, a log GitHub has already expired, or multiple simultaneous test
  failures in one job (only the first found is used) can all still produce
  `UNVERIFIED` — at that point you're back to reading the log yourself.
- **Aggregator/gate jobs are excluded by a name denylist** (`conclusion`,
  `all-green`, `ci-success`, `required-checks`, `success`), not by reading
  each job's actual `needs` graph. A repo using a differently-named gate job
  won't be recognized and may mask real signal underneath it.
- No pull request comment / GitHub Action integration yet — this is a
  read-only CLI you run and read.
- **`CONFIRMED` and `UNVERIFIED` are still leads, not verdicts.** Log
  verification raises confidence, it doesn't replace judgment — flakelens
  doesn't know *why* a test failed, only that the same one did, or didn't,
  repeat.

## License

Apache-2.0
