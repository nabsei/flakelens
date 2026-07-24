package main

import (
	"strings"
	"testing"

	"github.com/google/go-github/v66/github"
)

func TestCountDistinctOccurrences(t *testing.T) {
	const defaultBranch = "main"

	tests := []struct {
		name     string
		failures []isolatedFailure
		want     int
	}{
		{
			name: "same PR branch repeated collapses to one occurrence",
			failures: []isolatedFailure{
				{runID: 1, branch: "feat/musl-x86_64"},
				{runID: 2, branch: "feat/musl-x86_64"},
			},
			want: 1,
		},
		{
			name: "different PR branches each count",
			failures: []isolatedFailure{
				{runID: 1, branch: "fix-a"},
				{runID: 2, branch: "fix-b"},
				{runID: 3, branch: "fix-c"},
			},
			want: 3,
		},
		{
			name: "default branch failures never collapse, even with the same run ID twice",
			failures: []isolatedFailure{
				{runID: 10, branch: defaultBranch},
				{runID: 11, branch: defaultBranch},
				{runID: 12, branch: defaultBranch},
			},
			want: 3,
		},
		{
			name: "mix of default-branch and repeated PR-branch failures",
			failures: []isolatedFailure{
				{runID: 1, branch: "fix-postfix-err-completion"},
				{runID: 2, branch: "fix-postfix-err-completion"},
				{runID: 3, branch: "fix_assertion"},
				{runID: 4, branch: "feat/ungrammar-no-std-support"},
			},
			want: 3, // matches the rust-analyzer proc-macro-srv case
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countDistinctOccurrences(tt.failures, defaultBranch); got != tt.want {
				t.Errorf("countDistinctOccurrences() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestJobsForCorrelationExcludesAggregators(t *testing.T) {
	jobs := []*github.WorkflowJob{
		{Name: github.String("build")},
		{Name: github.String("test")},
		{Name: github.String("conclusion")},
		{Name: github.String("all-green")},
	}

	got := jobsForCorrelation(jobs)

	if len(got) != 2 {
		t.Fatalf("expected 2 non-aggregator jobs, got %d", len(got))
	}
	for _, j := range got {
		if aggregatorJobNames[j.GetName()] {
			t.Errorf("aggregator job %q should have been excluded", j.GetName())
		}
	}
}

func TestJobsForCorrelationExcludesLintJobs(t *testing.T) {
	jobs := []*github.WorkflowJob{
		{Name: github.String("build")},
		{Name: github.String("rustfmt")},
		{Name: github.String("clippy")},
		{Name: github.String("Rust (macos-latest)")},
		{Name: github.String("lint")},
		{Name: github.String("eslint")},
	}

	got := jobsForCorrelation(jobs)

	if len(got) != 2 {
		names := make([]string, len(got))
		for i, j := range got {
			names[i] = j.GetName()
		}
		t.Fatalf("expected 2 non-lint jobs, got %d: %v", len(got), names)
	}
	for _, j := range got {
		if looksLikeLintJob(j.GetName()) {
			t.Errorf("lint job %q should have been excluded", j.GetName())
		}
	}
}

func TestLooksLikeLintJob(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"rustfmt", true},
		{"clippy", true},
		{"eslint", true},
		{"Prettier Check", true},
		{"golangci-lint", true},
		{"Rust (ubuntu-latest)", false},
		{"proc-macro-srv", false},
		{"coverage-python", false},
	}
	for _, tt := range tests {
		if got := looksLikeLintJob(tt.name); got != tt.want {
			t.Errorf("looksLikeLintJob(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestExtractFailedTestName(t *testing.T) {
	tests := []struct {
		name string
		log  string
		want string
	}{
		{
			name: "pytest, real excerpt from pola-rs/polars coverage-python",
			log: "2026-07-23T19:34:38.4834670Z DEBUG Skipping file for mpmath\n" +
				"2026-07-23T20:08:27.2132280Z FAILED tests/unit/operations/namespaces/temporal/test_to_datetime.py::test_to_datetime - assert datetime.datetime(2000, 1, 1, 0, 0) == datetime.datetime(2000, 1, 1, 0, 1)\n" +
				"2026-07-23T20:08:27.2137130Z ===== 1 failed, 35161 passed =====\n",
			want: "tests/unit/operations/namespaces/temporal/test_to_datetime.py::test_to_datetime",
		},
		{
			name: "pytest FAILURES header only, real excerpt from encode/httpx (no short-summary FAILED line present)",
			log: "2026-05-20T16:27:47.6470576Z tests/test_wsgi.py ............                                          [100%]\n" +
				"2026-05-20T16:27:47.6471278Z =================================== FAILURES ===================================\n" +
				"2026-05-20T16:27:47.6472319Z ___________________________ test_write_timeout[trio] ___________________________\n" +
				"2026-05-20T16:27:47.6472598Z\n" +
				"2026-05-20T16:27:47.6512674Z =========================== short test summary info ============================\n" +
				"2026-05-20T16:27:47.6513178Z SKIPPED [1] tests/client/test_auth.py:273: reason\n" +
				"2026-05-20T16:27:47.6513709Z ================== 1 failed, 1416 passed, 1 skipped in 18.37s ==================\n",
			want: "test_write_timeout[trio]",
		},
		{
			name: "cargo test via file_test_runner, real excerpt from denoland/deno",
			log: "2026-07-23T22:59:01.9654556Z panicked at tests/specs/mod.rs:664:12:\n" +
				"2026-07-23T22:59:01.9654842Z pattern match failed\n" +
				"2026-07-23T22:59:01.9655281Z Test file: /home/runner/work/deno/deno/tests/specs/upgrade/stable/__test__.jsonc\n" +
				"2026-07-23T22:59:01.9655682Z failed tests:\n" +
				"2026-07-23T22:59:01.9655915Z     specs::upgrade::stable\n" +
				"2026-07-23T22:59:01.9657613Z 1 failed of 3044\n",
			want: "specs::upgrade::stable",
		},
		{
			name: "plain cargo test harness output",
			log:  "test some::module::test_name ... FAILED\n\nfailures:\n",
			want: "some::module::test_name",
		},
		{
			name: "go test",
			log:  "=== RUN   TestFoo\n--- FAIL: TestFoo (0.01s)\n    foo_test.go:12: assertion failed\n",
			want: "TestFoo",
		},
		{
			name: "ANSI color codes around the keyword don't break the match",
			log:  "2026-07-24T00:00:00.0000000Z \x1b[91mFAILED\x1b[0m \x1b[1mtests/foo.py::test_bar\x1b[0m - boom\n",
			want: "tests/foo.py::test_bar",
		},
		{
			name: "mocha, real excerpt from socketio/socket.io engine.io middlewares.js, includes both of mocha's two N) formats for the same failure",
			// mocha prints the failure twice: once inline as each spec runs
			// (flat, single line, no colon, immediately followed by the next
			// spec's result, a trap: it also starts with "N) text" and, if
			// picked, yields nothing useful), and once in the "failing"
			// section at the end (the real, structured one). Real capture
			// from a live run of this exact repo/job earlier in this
			// session, this is what caught the inline-marker bug.
			log: "2026-07-24T12:37:32.4711221Z   middlewares\n" +
				"2026-07-24T12:37:32.4779018Z     ✔ should apply middleware (polling)\n" +
				"2026-07-24T12:37:32.4810880Z     ✔ should apply middleware (websocket)\n" +
				"2026-07-24T12:37:32.4860418Z     1) should expose EventEmitter methods on the response object during upgrade (regression for pino-http)\n" +
				"2026-07-24T12:37:32.4886457Z     ✔ should apply all middlewares in order\n" +
				"2026-07-24T12:37:38.6725050Z   177 passing (7s)\n" +
				"2026-07-24T12:37:38.6725561Z   18 pending\n" +
				"2026-07-24T12:37:38.6725864Z   1 failing\n" +
				"2026-07-24T12:37:38.6729612Z   1) middlewares\n" +
				"2026-07-24T12:37:38.6730576Z        should expose EventEmitter methods on the response object during upgrade (regression for pino-http):\n" +
				"2026-07-24T12:37:38.6731679Z      Uncaught Error: expected undefined to be a function\n" +
				"2026-07-24T12:37:38.6734549Z       at Assertion.assert (/home/runner/work/socket.io/socket.io/node_modules/expect.js/index.js:96:13)\n",
			want: "should expose EventEmitter methods on the response object during upgrade (regression for pino-http)",
		},
		{
			name: "mocha with a nested describe: the deepest heading line wins, not the outer suite (only the it() title gets a colon, not the intermediate describe())",
			log: "2026-07-24T00:00:00.0000000Z   1) Outer suite\n" +
				"2026-07-24T00:00:00.0000000Z        Inner suite\n" +
				"2026-07-24T00:00:00.0000000Z          should do the specific thing:\n" +
				"2026-07-24T00:00:00.0000000Z      AssertionError: expected 1 to equal 2\n",
			want: "should do the specific thing",
		},
		{
			name: "unrecognized format yields no match",
			log:  "2026-07-24T00:00:00.0000000Z Build failed with exit code 1\n",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractFailedTestName(tt.log); got != tt.want {
				t.Errorf("extractFailedTestName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerdictFromTestNames(t *testing.T) {
	tests := []struct {
		name      string
		testNames []string
		snippets  []string
		wantHas   string // substring the verdict must contain
	}{
		{
			name:      "all occurrences agree on the same test",
			testNames: []string{"specs::upgrade::stable", "specs::upgrade::stable"},
			wantHas:   "CONFIRMED (2/2 logs agree): specs::upgrade::stable",
		},
		{
			name:      "occurrences disagree, like the polars coverage-python case",
			testNames: []string{"test_a", "test_b"},
			wantHas:   "REJECTED",
		},
		{
			name:      "no log could be parsed and snippets don't overlap",
			testNames: []string{"", ""},
			snippets:  []string{"unrelated content here", "something else entirely"},
			wantHas:   "UNVERIFIED",
		},
		{
			name:      "one extracted, one blank: still confirms on the one we have, but flagged weak",
			testNames: []string{"specs::upgrade::stable", ""},
			wantHas:   "CONFIRMED (weak, only 1/2 logs readable): specs::upgrade::stable",
		},
		{
			name:      "one readable out of many, like the httpx Python 3.12 case (1/8 logs readable, rest expired)",
			testNames: []string{"test_write_timeout[trio]", "", "", "", "", "", "", ""},
			wantHas:   "CONFIRMED (weak, only 1/8 logs readable): test_write_timeout[trio]",
		},
		{
			name:      "several agree, one blank: strong confirmation, not weak",
			testNames: []string{"foo::bar", "foo::bar", "foo::bar", ""},
			wantHas:   "CONFIRMED (3/4 logs agree): foo::bar",
		},
		{
			name:      "no exact test name anywhere, but failure text is similar: falls back to LIKELY",
			testNames: []string{"", ""},
			snippets: []string{
				"AssertionError: connection timed out after 30000ms waiting for upgrade handshake",
				"AssertionError: connection timed out after 30000ms during upgrade handshake step",
			},
			wantHas: "LIKELY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verdictFromTestNames(tt.testNames, tt.snippets); !strings.Contains(got, tt.wantHas) {
				t.Errorf("verdictFromTestNames() = %q, want it to contain %q", got, tt.wantHas)
			}
		})
	}
}

func TestSnippetsLookSimilar(t *testing.T) {
	tests := []struct {
		name     string
		snippets []string
		want     bool
	}{
		{
			name: "near-identical failure text across two occurrences",
			snippets: []string{
				"Error: connection reset by peer while fetching release manifest from update server",
				"Error: connection reset by peer while downloading release manifest from update server",
			},
			want: true,
		},
		{
			name: "unrelated failures, like the polars coverage-python case",
			snippets: []string{
				"AssertionError: DataFrames are different (value mismatch for column b_top_by_ca)",
				"AssertionError: Regex pattern did not match for test_unsupported_subquery_comparisons",
			},
			want: false,
		},
		{
			name:     "fewer than two usable snippets",
			snippets: []string{"only one snippet here", ""},
			want:     false,
		},
		{
			name: "one similar pair among three: the third breaks it, not just the closest pair",
			snippets: []string{
				"Error: connection reset by peer while fetching release manifest",
				"Error: connection reset by peer while fetching release manifest",
				"AssertionError: totally unrelated failure in a different test entirely",
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snippetsLookSimilar(tt.snippets); got != tt.want {
				t.Errorf("snippetsLookSimilar() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFailureSnippet(t *testing.T) {
	log := "2026-07-24T00:00:00.0000000Z Downloading dependencies\n" +
		"2026-07-24T00:00:00.0000000Z Compiling project\n" +
		"2026-07-24T00:00:00.0000000Z Running tests\n" +
		"2026-07-24T00:00:00.0000000Z AssertionError: expected 1 to equal 2\n" +
		"2026-07-24T00:00:00.0000000Z   at line 42\n"
	cleaned := cleanLog(log)
	got := failureSnippet(cleaned)
	if !strings.Contains(got, "AssertionError: expected 1 to equal 2") {
		t.Errorf("failureSnippet() = %q, want it to contain the assertion line", got)
	}

	if got := failureSnippet("nothing error-like in here at all just build output"); got == "" {
		// this line itself contains "error", so it should still be found;
		// the real no-match case is tested separately below.
		t.Errorf("failureSnippet() unexpectedly empty for a line containing 'error'")
	}

	if got := failureSnippet("clean build, nothing to see, all good"); got != "" {
		t.Errorf("failureSnippet() = %q, want empty for a log with no error-like line", got)
	}
}
