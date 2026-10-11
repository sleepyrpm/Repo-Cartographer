package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

func TestBuildPromptContainsTheEssentials(t *testing.T) {
	obs := contracts.Observation{
		RootPath: "/home/me/proj",
		Step:     3,
		Budget:   12,
		Tools: []contracts.ToolInfo{
			{Name: contracts.ToolReadFile, Description: "Read a file. args: {path}"},
			{Name: contracts.ToolSearch, Description: "Search the repo. args: {query}"},
		},
		Tree:    "cmd/\n  main.go\ninternal/app/app.go\n",
		Visited: []string{"README.md", "go.mod"},
		Unread:  []string{"cmd/main.go", "internal/app/app.go"},
		Transcript: []contracts.Exchange{{
			Call:   contracts.ToolCall{Name: contracts.ToolReadFile, Args: map[string]any{"path": "README.md"}},
			Result: "# Proj\nIt does things.",
		}},
	}

	p := BuildPrompt(obs)

	mustContain := []string{
		// job + state
		"/home/me/proj", "STEP: 3 of 12",
		// tools, verbatim from the registry
		contracts.ToolReadFile, contracts.ToolSearch,
		"Read a file. args: {path}", "Search the repo. args: {query}",
		// tree + bookkeeping
		"cmd/", "README.md", "go.mod", "cmd/main.go", "internal/app/app.go",
		// transcript
		"It does things.",
		// the output contract and its examples
		`{"tool":"<tool name>","args":{...},"done":false,"reason":"<short why>"}`,
		`{"tool":"","args":{},"done":true`,
		`{"tool":"read_file","args":{"path":"cmd/server/main.go"}`,
		"no markdown fences",
	}
	for _, want := range mustContain {
		if !strings.Contains(p, want) {
			t.Errorf("prompt is missing %q\n--- prompt ---\n%s", want, p)
		}
	}

	// It must never invent a tool that is not advertised.
	if strings.Contains(p, contracts.ToolFollowImports) {
		t.Error("prompt mentions a tool that is not in obs.Tools")
	}
}

func TestBuildPromptEmptyObservation(t *testing.T) {
	p := BuildPrompt(contracts.Observation{Step: 1, Budget: 5})
	if p == "" {
		t.Fatal("prompt must never be empty")
	}
	for _, want := range []string{"(none advertised)", "(none yet)", "(none left)", "JSON:"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing placeholder %q", want)
		}
	}
}

func TestBuildPromptIsBounded(t *testing.T) {
	// A deliberately abusive observation: a huge tree, 200 visited files, 200
	// unread files, 50 transcript entries with enormous results.
	big := strings.Repeat("internal/some/deeply/nested/package/file.go\n", 400)
	obs := contracts.Observation{
		RootPath: "/repo",
		Step:     9,
		Budget:   12,
		Tree:     big,
	}
	for _, tl := range testTools {
		tl.Description = strings.Repeat("blah ", 200)
		obs.Tools = append(obs.Tools, tl)
	}
	for i := 0; i < 200; i++ {
		obs.Visited = append(obs.Visited, fmt.Sprintf("pkg/visited_%03d.go", i))
		obs.Unread = append(obs.Unread, fmt.Sprintf("pkg/unread_%03d.go", i))
	}
	for i := 0; i < 50; i++ {
		obs.Transcript = append(obs.Transcript, contracts.Exchange{
			Call:   contracts.ToolCall{Name: contracts.ToolReadFile, Args: map[string]any{"path": fmt.Sprintf("f%02d.go", i)}},
			Result: strings.Repeat("x", 5000),
		})
	}

	p := BuildPrompt(obs)
	if len(p) > 6000 {
		t.Errorf("prompt is %d chars, want well under 6000 so CPU inference stays fast", len(p))
	}

	// Caps must actually bite.
	if strings.Contains(p, "pkg/unread_020.go") {
		t.Errorf("unread list was not capped at %d entries", maxUnreadListed)
	}
	if strings.Contains(p, "pkg/visited_020.go") {
		t.Errorf("visited list was not capped at %d entries", maxVisitedListed)
	}
	if !strings.Contains(p, "[truncated]") {
		t.Error("the oversized tree should have been visibly truncated")
	}
	// Only the last maxTranscriptSteps exchanges survive.
	if strings.Contains(p, "f00.go") {
		t.Error("old transcript entries should have been dropped")
	}
	if !strings.Contains(p, "f49.go") {
		t.Error("the most recent transcript entry must be present")
	}
	// And the output contract must still be there at the end.
	if !strings.HasSuffix(p, "JSON:") {
		t.Error("prompt should end with the JSON: cue")
	}
}

func TestBuildPromptShowsTranscriptErrors(t *testing.T) {
	obs := contracts.Observation{
		Step: 2, Budget: 5,
		Tools: testTools,
		Transcript: []contracts.Exchange{{
			Call: contracts.ToolCall{Name: contracts.ToolReadFile, Args: map[string]any{"path": "nope.go"}},
			Err:  "open nope.go: no such file or directory",
		}},
	}
	p := BuildPrompt(obs)
	if !strings.Contains(p, "ERROR: open nope.go: no such file") {
		t.Errorf("tool errors must be visible to the model:\n%s", p)
	}
}

func TestRetryPromptIsMoreForceful(t *testing.T) {
	base := BuildPrompt(contracts.Observation{Step: 1, Budget: 3, Tools: testTools})
	r := retryPrompt(base)
	if !strings.Contains(r, base) {
		t.Error("the retry must keep the full state so the model does not lose its place")
	}
	for _, want := range []string{"INVALID", "ONLY a single JSON object", "JSON:"} {
		if !strings.Contains(r, want) {
			t.Errorf("retry prompt missing %q", want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short string = %q", got)
	}
	got := truncate(strings.Repeat("a", 50), 10)
	if !strings.HasSuffix(got, "[truncated]") || len(got) > 30 {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abc", 0); got != "" {
		t.Errorf("truncate with n=0 = %q", got)
	}
	// Multi-byte input must not be cut mid-rune.
	if got := truncate("héllo wörld", 4); !strings.HasPrefix(got, "héll") {
		t.Errorf("truncate mangled runes: %q", got)
	}
}
