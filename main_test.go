package main

import (
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
