// flakelens finds jobs that are flaky — not broken — by reading GitHub
// Actions history a repo already has, no rerun or test-report artifact
// required.
//
// The signal: within a single workflow run, most jobs share the same commit,
// so a genuine code problem (a real compile error, a real lint violation)
// tends to fail most or all jobs in that run together. A job that fails
// *alone*, while its sibling jobs in the same run pass, is not explained by
// "the code was broken" — it's isolated, and isolated, repeated failures are
// exactly what flaky (non-deterministic) jobs look like.
//
// This was chosen after two rejected mechanisms, verified empirically against
// rust-lang/rust-analyzer before writing this: (1) explicit reruns
// (run_attempt > 1) are too rare to find in a practical history window —
// nobody clicks "re-run failed jobs" often enough; (2) per-job raw failure
// rate alone is misleading — several jobs can share the same failure rate
// because they all failed together on the same handful of genuinely broken
// runs, which isolated-failure counting correctly excludes.
//
// A third gap surfaced by testing against denoland/deno: a PR branch can fail
// the same isolated job twice in a row for a perfectly deterministic reason —
// the author is mid-debugging still-broken new code (observed: a new musl
// build target failing on the same toolchain relocation error across two
// pushes to the same PR). That looks identical to flakiness under "isolated +
// repeated". The first fix tried (drop all pull_request-event runs) was too
// blunt: re-testing against rust-lang/rust-analyzer showed the original
// proc-macro-srv signal came from four *different* PR branches by different
// authors — excluding PR runs entirely threw that away, even though
// cross-branch repetition is if anything a stronger flakiness signal than a
// post-merge repeat (master/main is reused forever, so "same branch" can't be
// used to distinguish repeat merges from a single in-progress PR there).
//
// The actual discriminator is "same branch, still being iterated on" vs.
// "different branches/commits, unrelated to each other" — not PR-vs-push.
// Repeated isolated failures on the *same* non-default branch collapse to one
// occurrence (presumptively the same still-broken code, not flakiness);
// failures on the default branch always count individually, since a rerun of
// "master" is never a rerun of the same in-progress fix.
//
// A fifth gap, found the same way (testing against pola-rs/polars): this tool
// operates at job granularity, but a job can be a monolithic test suite (e.g.
// "coverage-python" running ~35k tests). Two unrelated PRs each breaking one
// unrelated test of their own (observed: a temporal-handling PR failing a
// datetime test, a parquet-filter PR failing an iceberg/parquet test) get
// counted as the same job repeating, even though they're two different real
// bugs in two different tests that just happen to share a job name.
//
// Mitigated, not solved: for jobs that clear the isolated-failure threshold,
// flakelens now downloads each occurrence's log and tries to extract the
// actual failing test name (a handful of regexes covering pytest, cargo
// test, and go test — not a real parser per framework, which is the bigger
// "adapter" cost buildline deliberately took on and this deliberately
// hasn't). If the extracted names disagree across occurrences, the job is
// dropped — that's the polars case, not flakiness. If a name can't be
// extracted at all (unrecognized log format), the finding is kept but
// labeled UNVERIFIED rather than presented at the same confidence as a
// confirmed match. This is best-effort pattern matching, not a real parser:
// it will miss test frameworks it doesn't recognize, and a job with multiple
// simultaneous test failures only looks at the first one found in the log.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/google/go-github/v66/github"
)

// githubPerPageMax is GitHub's hard cap on the per_page query parameter;
// requesting more just gets silently clamped to this by the API.
const githubPerPageMax = 100

// maxCoFailures: a failing job counts as "isolated" (not explained by a
// broad, correlated failure) only when at most this many *other* jobs in the
// same run also failed. Zero is the strict, clean signal; a small tolerance
// allows for one unrelated pre-existing failure without excusing a job that
// clearly failed alongside a real, broad breakage.
const maxCoFailures = 0

// minIsolatedFailures: report a job only once it has failed in isolation this
// many times — one isolated failure could still be a one-off; a repeated
// pattern is what makes it a flakiness candidate rather than noise.
const minIsolatedFailures = 2

type isolatedFailure struct {
	runID   int64
	jobID   int64
	headSHA string
	branch  string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: flakelens <owner/repo> [max-runs]")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "flakelens:", err)
		os.Exit(1)
	}
}

func run(ownerRepo string, rest []string) error {
	parts := strings.SplitN(ownerRepo, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("expected owner/repo, got %q", ownerRepo)
	}
	owner, repo := parts[0], parts[1]

	maxRuns := 100
	if len(rest) > 0 {
		fmt.Sscanf(rest[0], "%d", &maxRuns)
	}

	client := github.NewClient(nil)
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		client = client.WithAuthToken(token)
	}
	ctx := context.Background()

	repoInfo, _, err := client.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return friendlyError(err, ownerRepo)
	}
	defaultBranch := repoInfo.GetDefaultBranch()

	var runs []*github.WorkflowRun
	opts := &github.ListWorkflowRunsOptions{
		Status:      "completed",
		ListOptions: github.ListOptions{PerPage: githubPerPageMax},
	}
	for len(runs) < maxRuns {
		runsResp, resp, err := client.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
		if err != nil {
			return friendlyError(err, ownerRepo)
		}
		runs = append(runs, runsResp.WorkflowRuns...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	if len(runs) > maxRuns {
		runs = runs[:maxRuns]
	}

	byJob := make(map[string][]isolatedFailure)

	for _, wr := range runs {
		jobsResp, _, err := client.Actions.ListWorkflowJobs(ctx, owner, repo, wr.GetID(), nil)
		if err != nil {
			var rateErr *github.RateLimitError
			if errors.As(err, &rateErr) {
				return friendlyError(err, ownerRepo)
			}
			fmt.Fprintf(os.Stderr, "flakelens: warning: skipping run %d: %v\n", wr.GetID(), err)
			continue
		}
		jobs := jobsForCorrelation(jobsResp.Jobs)
		if len(jobs) < 2 {
			continue // no siblings to compare against
		}

		totalFailures := 0
		for _, j := range jobs {
			if j.GetConclusion() == "failure" {
				totalFailures++
			}
		}

		for _, j := range jobs {
			if j.GetConclusion() != "failure" {
				continue
			}
			coFailures := totalFailures - 1 // other jobs in this run that also failed
			if coFailures > maxCoFailures {
				continue // this run looks broadly broken, not isolated
			}
			name := j.GetName()
			byJob[name] = append(byJob[name], isolatedFailure{
				runID:   wr.GetID(),
				jobID:   j.GetID(),
				headSHA: wr.GetHeadSHA(),
				branch:  wr.GetHeadBranch(),
			})
		}
	}

	type report struct {
		name     string
		failures []isolatedFailure
	}
	var results []report
	for name, failures := range byJob {
		if countDistinctOccurrences(failures, defaultBranch) >= minIsolatedFailures {
			results = append(results, report{name, failures})
		}
	}
	sort.Slice(results, func(i, j int) bool { return len(results[i].failures) > len(results[j].failures) })

	if len(results) == 0 {
		fmt.Println("No jobs with repeated isolated failures found in the observed run history.")
		return nil
	}

	httpClient := &http.Client{}
	fmt.Printf("Jobs that failed in isolation (siblings in the same run passed) on %d+ distinct branches/occasions:\n\n", minIsolatedFailures)
	for _, r := range results {
		verdict, testNames := verifyAgainstLogs(ctx, client, httpClient, owner, repo, r.failures)
		fmt.Printf("- %s: %d isolated failures (%d distinct occurrences) [%s]\n",
			r.name, len(r.failures), countDistinctOccurrences(r.failures, defaultBranch), verdict)
		for i, f := range r.failures {
			testName := ""
			if i < len(testNames) && testNames[i] != "" {
				testName = " — " + testNames[i]
			}
			fmt.Printf("    commit %s, branch %s, https://github.com/%s/%s/actions/runs/%d%s\n",
				f.headSHA[:min(8, len(f.headSHA))], f.branch, owner, repo, f.runID, testName)
		}
	}
	return nil
}

// verifyAgainstLogs downloads the log for each occurrence and tries to
// extract the actual failing test name (see the pattern list on
// failedTestPatterns). It returns a human-readable verdict plus the
// per-occurrence test names (same order/length as the input, empty string
// where extraction failed) so the caller can print them alongside each run.
//
//   - CONFIRMED: <name>   — every occurrence where a name was found agrees
//   - REJECTED: different tests failed (<name a> vs <name b>) — likely not
//     flakiness, likely the "monolithic test suite" job-granularity problem
//   - UNVERIFIED — couldn't extract a test name from any occurrence's log;
//     the job-name-level correlation still stands, just unconfirmed
func verifyAgainstLogs(ctx context.Context, client *github.Client, httpClient *http.Client, owner, repo string, failures []isolatedFailure) (verdict string, testNames []string) {
	testNames = make([]string, len(failures))
	for i, f := range failures {
		name, err := fetchFailedTestName(ctx, client, httpClient, owner, repo, f.jobID)
		if err != nil {
			continue // log unavailable or unreadable; leave this occurrence blank
		}
		testNames[i] = name
	}
	return verdictFromTestNames(testNames), testNames
}

// verdictFromTestNames is the pure decision logic behind verifyAgainstLogs,
// split out so it can be unit tested without downloading anything: given the
// (possibly partially empty) list of test names extracted per occurrence, it
// decides whether the occurrences agree, disagree, or yielded nothing usable.
//
// A common failure mode this has to account for (found testing against
// encode/httpx): GitHub only retains job logs for a limited window (90 days
// by default), so by the time an isolated-failure candidate is old enough to
// have accumulated multiple occurrences, several of those occurrences' logs
// may have already expired — leaving only one or two readable out of many.
// Agreement across 1 readable log out of 8 is real, but much weaker evidence
// than agreement across, say, 2 out of 2 — the verdict says so explicitly
// rather than presenting both as equally "CONFIRMED."
func verdictFromTestNames(testNames []string) string {
	found := map[string]bool{}
	readable := 0
	for _, name := range testNames {
		if name != "" {
			found[name] = true
			readable++
		}
	}

	switch len(found) {
	case 0:
		return "UNVERIFIED"
	case 1:
		var name string
		for n := range found {
			name = n
		}
		if readable == 1 {
			return fmt.Sprintf("CONFIRMED (weak — only 1/%d logs readable): %s", len(testNames), name)
		}
		return fmt.Sprintf("CONFIRMED (%d/%d logs agree): %s", readable, len(testNames), name)
	default:
		names := make([]string, 0, len(found))
		for n := range found {
			names = append(names, n)
		}
		sort.Strings(names)
		return "REJECTED: different tests failed (" + strings.Join(names, " vs ") + ")"
	}
}

func fetchFailedTestName(ctx context.Context, client *github.Client, httpClient *http.Client, owner, repo string, jobID int64) (string, error) {
	logURL, _, err := client.Actions.GetWorkflowJobLogs(ctx, owner, repo, jobID, 0)
	if err != nil {
		return "", fmt.Errorf("getting log URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, logURL.String(), nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading log: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading log: unexpected status %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return extractFailedTestName(string(body)), nil
}

// friendlyError turns GitHub's rate-limit error into an actionable message.
// Unauthenticated requests get 60/hour, which a single run of flakelens on
// an active repo can burn through in one shot (one call per workflow run
// examined); an authenticated token raises that to 5000/hour.
func friendlyError(err error, ownerRepo string) error {
	var rateErr *github.RateLimitError
	if errors.As(err, &rateErr) {
		if os.Getenv("GITHUB_TOKEN") == "" {
			return fmt.Errorf("GitHub API rate limit hit (unauthenticated requests are capped at 60/hour): set GITHUB_TOKEN to raise this to 5000/hour, e.g. GITHUB_TOKEN=$(gh auth token) flakelens %s", ownerRepo)
		}
		return fmt.Errorf("GitHub API rate limit hit even with GITHUB_TOKEN set: %w", err)
	}
	return err
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// countDistinctOccurrences collapses repeated isolated failures that share
// the same non-default branch into a single occurrence, since those are
// presumptively the same still-broken code being iterated on within one PR,
// not independent evidence of flakiness. Failures on the default branch are
// never collapsed together: a rerun of "main"/"master" is a distinct commit
// each time, never a rerun of the same in-progress fix.
func countDistinctOccurrences(failures []isolatedFailure, defaultBranch string) int {
	seen := make(map[string]bool)
	for _, f := range failures {
		key := f.branch
		if key == defaultBranch {
			key = fmt.Sprintf("run-%d", f.runID)
		}
		seen[key] = true
	}
	return len(seen)
}

// aggregatorJobNames are gate/summary jobs that depend on every other job and
// so fail whenever ANY sibling fails. Left in, they'd co-fail alongside every
// real isolated failure and mask it under maxCoFailures, while never being
// isolated themselves. This is a name-based denylist for now; the more
// general fix is reading each job's "needs" graph to detect fan-in gates
// structurally instead of by name.
var aggregatorJobNames = map[string]bool{
	"conclusion":      true,
	"all-green":       true,
	"ci-success":      true,
	"required-checks": true,
	"success":         true,
}

// lintJobNamePatterns are substrings of job names (checked case-insensitively)
// that commonly indicate a deterministic lint/format check: rustfmt, clippy,
// eslint, prettier, and the like. The core correlation signal — "isolated
// among its siblings" — assumes siblings are testing the same thing, so a
// lone failure is surprising. That assumption breaks for jobs that check
// something orthogonal to the rest of the matrix: a lint/format job fails or
// passes based purely on whether that PR's diff satisfies the rule, entirely
// independent of whether the build/test jobs pass. Found by reporting
// rust-analyzer's rustfmt as a flakiness candidate and being correctly told
// by a maintainer it was "expected to fail without relation to other jobs
// since they just check other things":
// https://github.com/rust-lang/rust-analyzer/issues/22904
var lintJobNamePatterns = []string{
	"fmt",
	"format",
	"lint",
	"clippy",
	"prettier",
	"eslint",
	"checkstyle",
	"stylelint",
	"rubocop",
	"flake8",
	"ruff",
	"black",
	"shellcheck",
}

func looksLikeLintJob(name string) bool {
	lower := strings.ToLower(name)
	for _, pattern := range lintJobNamePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// logLinePrefix strips the "<ISO8601 timestamp> " GitHub Actions prefixes
// every raw log line with, so the patterns below can match log content
// starting at the beginning of a line rather than after a variable-length
// timestamp.
var logLinePrefix = regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d+Z `)

// ansiEscape strips terminal color codes, which several test runners emit
// even in non-interactive CI logs and which would otherwise sit between a
// matched keyword and the test name.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;]*m")

// failedTestPatterns extract a failing test's identifier from a cleaned log,
// tried in order, first match wins. Best-effort coverage of a few common
// frameworks — see the package doc comment for what this does and doesn't
// give you.
var failedTestPatterns = []*regexp.Regexp{
	// pytest short summary: "FAILED tests/path/test_x.py::test_name - AssertionError: ..."
	// Not always present — some pytest configs/versions only print the
	// FAILURES section below, not this summary line.
	regexp.MustCompile(`(?m)^FAILED (\S+)`),
	// pytest FAILURES section header: "____ test_name[param] ____" (found
	// testing against encode/httpx, where the short summary line above was
	// missing from several logs but this header always was present). No file
	// path in this one, just the bare test name — a weaker identifier than
	// the short-summary form above (same name possible in two files), which
	// is why it's tried second, not first.
	regexp.MustCompile(`(?m)^_{3,}\s+(.+?)\s+_{3,}\s*$`),
	// cargo test via file_test_runner (used by e.g. deno's specs tests):
	// "failed tests:\n    specs::upgrade::stable"
	regexp.MustCompile(`(?m)^failed tests:\r?\n\s+(\S+)`),
	// plain `cargo test` / rustc's built-in harness: "test some::name ... FAILED"
	regexp.MustCompile(`(?m)^test (\S+) \.\.\. FAILED`),
	// go test: "--- FAIL: TestName"
	regexp.MustCompile(`(?m)^--- FAIL: (\S+)`),
}

func extractFailedTestName(log string) string {
	cleaned := ansiEscape.ReplaceAllString(logLinePrefix.ReplaceAllString(log, ""), "")
	for _, pattern := range failedTestPatterns {
		if m := pattern.FindStringSubmatch(cleaned); m != nil {
			return m[1]
		}
	}
	return ""
}

func jobsForCorrelation(jobs []*github.WorkflowJob) []*github.WorkflowJob {
	out := make([]*github.WorkflowJob, 0, len(jobs))
	for _, j := range jobs {
		if aggregatorJobNames[j.GetName()] || looksLikeLintJob(j.GetName()) {
			continue
		}
		out = append(out, j)
	}
	return out
}
