package main

import (
	"testing"

	"handshake/internal/authoring"
)

func TestRecommendedAuthoringRunner_PrefersClaude(t *testing.T) {
	available := []authoring.DetectedRunner{
		{Runner: authoring.RunnerCodex},
		{Runner: authoring.RunnerClaude},
		{Runner: authoring.RunnerOpenCode},
	}

	if got := recommendedAuthoringRunner(available); got != authoring.RunnerClaude {
		t.Fatalf("recommendedAuthoringRunner() = %q, want %q", got, authoring.RunnerClaude)
	}
}

func TestRecommendedAuthoringRunner_UsesFirstAvailable(t *testing.T) {
	available := []authoring.DetectedRunner{
		{Runner: authoring.RunnerOpenCode},
		{Runner: authoring.RunnerCodex},
	}

	if got := recommendedAuthoringRunner(available); got != authoring.RunnerOpenCode {
		t.Fatalf("recommendedAuthoringRunner() = %q, want %q", got, authoring.RunnerOpenCode)
	}
}

func TestParseAuthoringRunnerSelection(t *testing.T) {
	available := []authoring.DetectedRunner{
		{Runner: authoring.RunnerClaude},
		{Runner: authoring.RunnerOpenCode},
	}

	tests := []struct {
		name  string
		input string
		want  authoring.Runner
		ok    bool
	}{
		{name: "blank uses recommendation", input: "", want: authoring.RunnerClaude, ok: true},
		{name: "selects available writer", input: "2", want: authoring.RunnerOpenCode, ok: true},
		{name: "rejects zero", input: "0", ok: false},
		{name: "rejects missing writer", input: "3", ok: false},
		{name: "rejects text", input: "claude", ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseAuthoringRunnerSelection(test.input, available, authoring.RunnerClaude)
			if got != test.want || ok != test.ok {
				t.Fatalf("parseAuthoringRunnerSelection(%q) = (%q, %t), want (%q, %t)", test.input, got, ok, test.want, test.ok)
			}
		})
	}
}
