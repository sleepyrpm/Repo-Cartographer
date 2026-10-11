package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNewValidatesOptions(t *testing.T) {
	reg := newRegistry()
	dec := script()

	if _, err := New(Options{Registry: reg, Decider: dec}); err == nil {
		t.Fatal("expected an error for an empty Root")
	}
	if _, err := New(Options{Root: "/r", Decider: dec}); err == nil {
		t.Fatal("expected an error for a nil Registry")
	}
	if _, err := New(Options{Root: "/r", Registry: reg}); err == nil {
		t.Fatal("expected an error for a nil Decider")
	}
	a, err := New(Options{Root: "/r", Registry: reg, Decider: dec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.budget != DefaultBudget {
		t.Fatalf("budget = %d, want DefaultBudget (%d)", a.budget, DefaultBudget)
	}
	if a.res.Imports == nil || a.res.RawImports == nil {
		t.Fatal("Imports/RawImports must never be nil")
	}
}

// ---------------------------------------------------------------------------
// happy path: bootstrap -> read_file -> auto follow_imports -> done
// ---------------------------------------------------------------------------

func happyRegistry() (*stubRegistry, *stubTool, *stubTool) {
	tree := treeTool("cmd/\n  app/\n    main.go\ninternal/\n  x/\n    x.go",
		contracts.RepoFile{Path: "internal/x/x.go", Lang: "go", Size: 50},
		contracts.RepoFile{Path: "cmd/app/main.go", Lang: "go", Size: 100},
	)
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{
			Text: "// Package main wires the cartographer together.\npackage main\n\nimport \"github.com/me/p/internal/x\"\n",
			Meta: map[string]any{
				"path":      args["path"],
				"lang":      "go",
				"size":      412,
				"truncated": false,
				"imports":   []string{"github.com/me/p/internal/x"},
			},
		}, nil
	})
	follow := tool(contracts.ToolFollowImports, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{
			Text: "resolved 1",
			Meta: map[string]any{
				"imports":  []string{"github.com/me/p/internal/x"},
				"resolved": []string{"internal/x/x.go"},
			},
		}, nil
	})
	return newRegistry(tree, read, follow), read, follow
}

func TestRunHappyPath(t *testing.T) {
	reg, read, follow := happyRegistry()
	dec := script(
		readStep("cmd/app/main.go", "looks like the entrypoint"),
		doneStep("I understand the layout now"),
	)
	a, err := New(Options{Root: "/repo", Registry: reg, Decider: dec, Budget: 10, Clock: tickClock()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if res.StoppedBecause != "model signalled done" {
		t.Errorf("StoppedBecause = %q", res.StoppedBecause)
	}
	if res.StepsUsed != 2 {
		t.Errorf("StepsUsed = %d, want 2", res.StepsUsed)
	}
	if res.RootPath != "/repo" {
		t.Errorf("RootPath = %q", res.RootPath)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", res.Duration)
	}

	// Steps: bootstrap repo_tree, read_file, implicit follow_imports.
	wantCalls := []string{contracts.ToolRepoTree, contracts.ToolReadFile, contracts.ToolFollowImports}
	if len(res.Steps) != len(wantCalls) {
		t.Fatalf("len(Steps) = %d, want %d: %+v", len(res.Steps), len(wantCalls), res.Steps)
	}
	for i, want := range wantCalls {
		if res.Steps[i].Call.Name != want {
			t.Errorf("Steps[%d].Call.Name = %q, want %q", i, res.Steps[i].Call.Name, want)
		}
		if res.Steps[i].Err != "" {
			t.Errorf("Steps[%d].Err = %q, want empty", i, res.Steps[i].Err)
		}
	}
	if len(res.Decisions) != len(res.Steps) {
		t.Fatalf("Decisions (%d) must stay parallel to Steps (%d)", len(res.Decisions), len(res.Steps))
	}
	if !strings.Contains(res.Decisions[0].Reason, "bootstrap") {
		t.Errorf("Decisions[0].Reason = %q, want a bootstrap marker", res.Decisions[0].Reason)
	}
	if res.Decisions[1].Reason != "looks like the entrypoint" {
		t.Errorf("Decisions[1].Reason = %q", res.Decisions[1].Reason)
	}
	if !strings.Contains(res.Decisions[2].Reason, "auto-follow") {
		t.Errorf("Decisions[2].Reason = %q, want an auto-follow marker", res.Decisions[2].Reason)
	}

	if want := []string{"cmd/app/main.go"}; !reflect.DeepEqual(res.Visited, want) {
		t.Errorf("Visited = %v, want %v", res.Visited, want)
	}
	if want := []string{"internal/x/x.go"}; !reflect.DeepEqual(res.Imports["cmd/app/main.go"], want) {
		t.Errorf("Imports = %v, want %v", res.Imports, want)
	}
	if want := []string{"github.com/me/p/internal/x"}; !reflect.DeepEqual(res.RawImports["cmd/app/main.go"], want) {
		t.Errorf("RawImports = %v, want %v", res.RawImports, want)
	}
	if len(res.Findings) != 1 || res.Findings[0].Path != "cmd/app/main.go" {
		t.Fatalf("Findings = %+v", res.Findings)
	}
	note := res.Findings[0].Note
	if !strings.Contains(note, "package main") || !strings.Contains(note, "1 imports") {
		t.Errorf("Finding note = %q, want the declaration and the import count", note)
	}
	if len(note) > maxFindingNote {
		t.Errorf("Finding note is %d chars, cap is %d", len(note), maxFindingNote)
	}

	// Inventory: seeded from repo_tree and enriched by the read.
	byPath := map[string]contracts.RepoFile{}
	for _, f := range res.Files {
		byPath[f.Path] = f
	}
	if len(byPath) != 2 {
		t.Errorf("Files = %+v, want 2 entries", res.Files)
	}
	if got := byPath["cmd/app/main.go"]; got.Size != 412 || got.Lang != "go" {
		t.Errorf("main.go inventory = %+v, want size 412 lang go", got)
	}

	// Tool call plumbing.
	if len(read.calls) != 1 || read.calls[0]["path"] != "cmd/app/main.go" {
		t.Errorf("read_file calls = %v", read.calls)
	}
	if len(follow.calls) != 1 || follow.calls[0]["path"] != "cmd/app/main.go" {
		t.Errorf("follow_imports calls = %v", follow.calls)
	}

	// Observations handed to the model.
	if dec.calls() != 2 {
		t.Fatalf("decider called %d times, want 2", dec.calls())
	}
	first := dec.seen[0]
	if first.Step != 1 || first.Budget != 10 || first.RootPath != "/repo" {
		t.Errorf("first observation = %+v", first)
	}
	if !strings.Contains(first.Tree, "main.go") {
		t.Errorf("observation Tree = %q, want the bootstrap tree text", first.Tree)
	}
	if len(first.Tools) != 3 {
		t.Errorf("observation Tools = %+v, want 3", first.Tools)
	}
	if len(first.Unread) != 2 || first.Unread[0] != "cmd/app/main.go" {
		t.Errorf("Unread = %v, want the entrypoint first", first.Unread)
	}
	// The bootstrap repo_tree is the agent's own step, not the model's, so it
	// is kept out of the transcript (the tree itself is in Observation.Tree).
	if len(first.Transcript) != 0 {
		t.Errorf("first Transcript = %+v, want empty on the first decision", first.Transcript)
	}
	second := dec.seen[1]
	if !reflect.DeepEqual(second.Visited, []string{"cmd/app/main.go"}) {
		t.Errorf("second observation Visited = %v", second.Visited)
	}
	if len(second.Unread) != 1 || second.Unread[0] != "internal/x/x.go" {
		t.Errorf("second observation Unread = %v", second.Unread)
	}
	// Only the model's own read is shown back; the agent's auto-follow of that
	// file's imports is audit-log-only.
	if len(second.Transcript) != 1 || second.Transcript[0].Call.Name != contracts.ToolReadFile {
		t.Errorf("second Transcript = %+v, want just the model's own read", second.Transcript)
	}
	if len(res.Steps) != 3 {
		t.Errorf("Result.Steps = %d, want 3 (bootstrap + read + auto-follow)", len(res.Steps))
	}
}

// ---------------------------------------------------------------------------
// stop conditions
// ---------------------------------------------------------------------------

func TestBudgetExhaustion(t *testing.T) {
	tree := treeTool("tree", contracts.RepoFile{Path: "a.go"})
	search := tool(contracts.ToolSearch, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "no matches", Meta: map[string]any{"matches": []string{}, "count": 0}}, nil
	})
	// Searches for a fresh pattern every step, so the no-progress guard never
	// fires and only the budget can stop the run.
	brain := &funcDecider{fn: func(_ context.Context, obs contracts.Observation) (contracts.Decision, error) {
		return contracts.Decision{
			Tool:   contracts.ToolSearch,
			Args:   map[string]any{"pattern": fmt.Sprintf("pat-%d", obs.Step)},
			Reason: "keep looking",
		}, nil
	}}
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, search), Decider: brain, Budget: 4})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StoppedBecause != "step budget exhausted" {
		t.Errorf("StoppedBecause = %q", res.StoppedBecause)
	}
	if res.StepsUsed != 4 || brain.n != 4 {
		t.Errorf("StepsUsed = %d, decider calls = %d, want 4 and 4", res.StepsUsed, brain.n)
	}
	if want := []string{"pat-1", "pat-2", "pat-3", "pat-4"}; !reflect.DeepEqual(res.Searches, want) {
		t.Errorf("Searches = %v, want %v", res.Searches, want)
	}
}

func TestNoProgressGuard(t *testing.T) {
	tree := treeTool("tree", contracts.RepoFile{Path: "a.go"})
	list := tool(contracts.ToolListDir, func(args map[string]any) (contracts.ToolResult, error) {
		// Only ever reports things we already know: zero new information.
		return contracts.ToolResult{
			Text: "a.go",
			Meta: map[string]any{"path": ".", "entries": []string{"a.go", "sub/"}},
		}, nil
	})
	// Each call asks about a different directory, so vet()'s repeated-call
	// repair does not fire: these are distinct questions that each happen to
	// teach the agent nothing. That is precisely what the stall guard is for.
	dirs := []string{".", "sub", "sub/deeper"}
	brain := &funcDecider{fn: func(_ context.Context, obs contracts.Observation) (contracts.Decision, error) {
		dir := dirs[(obs.Step-1)%len(dirs)]
		return contracts.Decision{Tool: contracts.ToolListDir, Args: map[string]any{"path": dir}, Reason: "look around"}, nil
	}}
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, list), Decider: brain, Budget: 20})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StoppedBecause != "no progress" {
		t.Errorf("StoppedBecause = %q, want \"no progress\"", res.StoppedBecause)
	}
	if res.StepsUsed != stallLimit {
		t.Errorf("StepsUsed = %d, want %d", res.StepsUsed, stallLimit)
	}
}

func TestContextCancelledBeforeFirstDecision(t *testing.T) {
	reg, _, _ := happyRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, _ := New(Options{Root: "/r", Registry: reg, Decider: script(doneStep("never asked"))})
	res, err := a.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if res == nil {
		t.Fatal("Run must return the partial result alongside the error")
	}
	if !strings.HasPrefix(res.StoppedBecause, "context cancelled") {
		t.Errorf("StoppedBecause = %q", res.StoppedBecause)
	}
	// The bootstrap still happened, so the partial result is still useful.
	if len(res.Steps) != 1 || len(res.Files) != 2 {
		t.Errorf("partial result lost the bootstrap: %d steps, %d files", len(res.Steps), len(res.Files))
	}
	if res.StepsUsed != 0 {
		t.Errorf("StepsUsed = %d, want 0", res.StepsUsed)
	}
}

func TestContextCancelledDuringRun(t *testing.T) {
	reg, _, _ := happyRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	brain := &funcDecider{fn: func(_ context.Context, obs contracts.Observation) (contracts.Decision, error) {
		if obs.Step == 2 {
			cancel()
		}
		return contracts.Decision{
			Tool:   contracts.ToolReadFile,
			Args:   map[string]any{"path": obs.Unread[0]},
			Reason: "next file",
		}, nil
	}}
	a, _ := New(Options{Root: "/r", Registry: reg, Decider: brain, Budget: 10})
	res, err := a.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(res.Visited) != 1 {
		t.Errorf("Visited = %v, want the one file read before cancellation", res.Visited)
	}
	if !strings.HasPrefix(res.StoppedBecause, "context cancelled") {
		t.Errorf("StoppedBecause = %q", res.StoppedBecause)
	}
}

// ---------------------------------------------------------------------------
// resilience
// ---------------------------------------------------------------------------

func TestDeciderErrorFallsBackAndContinues(t *testing.T) {
	reg, read, _ := happyRegistry()
	dec := script(
		scriptStep{err: errors.New("ollama is not running")},
		doneStep("enough"),
	)
	a, _ := New(Options{Root: "/r", Registry: reg, Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run must survive a Decider error, got %v", err)
	}
	if res.StoppedBecause != "model signalled done" {
		t.Errorf("StoppedBecause = %q - the loop should have continued", res.StoppedBecause)
	}
	if len(read.calls) != 1 || read.calls[0]["path"] != "cmd/app/main.go" {
		t.Errorf("fallback should have read the top-ranked unread file, got %v", read.calls)
	}
	if !strings.Contains(res.Decisions[1].Reason, "agent fallback") ||
		!strings.Contains(res.Decisions[1].Reason, "decider error") {
		t.Errorf("Decisions[1].Reason = %q, want the fallback to be explained", res.Decisions[1].Reason)
	}
}

func TestUnknownToolIsSubstituted(t *testing.T) {
	reg, read, _ := happyRegistry()
	dec := script(
		scriptStep{dec: contracts.Decision{Tool: "summarise_repo", Reason: "invented tool"}},
		doneStep("enough"),
	)
	a, _ := New(Options{Root: "/r", Registry: reg, Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The rejected call is recorded with the refusal in Err, so the model can
	// see on its next turn that the agent overrode it, followed by the
	// substituted read.
	if res.Steps[1].Call.Name != "summarise_repo" || !strings.Contains(res.Steps[1].Err, "REFUSED") {
		t.Errorf("Steps[1] = %+v, want the refused call recorded", res.Steps[1])
	}
	if res.Steps[2].Call.Name != contracts.ToolReadFile {
		t.Errorf("Steps[2] = %+v, want a substituted read_file", res.Steps[2])
	}
	if !strings.Contains(res.Decisions[2].Reason, "unknown tool") {
		t.Errorf("Decisions[2].Reason = %q", res.Decisions[2].Reason)
	}
	if len(read.calls) != 1 {
		t.Errorf("read_file calls = %v", read.calls)
	}
	if res.StoppedBecause != "model signalled done" {
		t.Errorf("StoppedBecause = %q", res.StoppedBecause)
	}
}

func TestToolErrorIsRecordedAndLoopContinues(t *testing.T) {
	tree := treeTool("tree", contracts.RepoFile{Path: "a.go"}, contracts.RepoFile{Path: "b.go"})
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		if args["path"] == "a.go" {
			return contracts.ToolResult{}, errors.New("permission denied")
		}
		return contracts.ToolResult{Text: "package b\n", Meta: map[string]any{"path": "b.go", "lang": "go"}}, nil
	})
	dec := script(readStep("a.go", "try a"), readStep("b.go", "try b"), doneStep("ok"))
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read), Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Steps[1].Err, "permission denied") {
		t.Errorf("Steps[1].Err = %q", res.Steps[1].Err)
	}
	if want := []string{"b.go"}; !reflect.DeepEqual(res.Visited, want) {
		t.Errorf("Visited = %v, want %v (the failed read must not count)", res.Visited, want)
	}
	if res.StepsUsed != 3 || res.StoppedBecause != "model signalled done" {
		t.Errorf("StepsUsed = %d, StoppedBecause = %q", res.StepsUsed, res.StoppedBecause)
	}
}

func TestPanickingToolIsRecovered(t *testing.T) {
	tree := treeTool("tree", contracts.RepoFile{Path: "a.go"}, contracts.RepoFile{Path: "b.go"})
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		panic("index out of range")
	})
	dec := script(readStep("a.go", "boom"), doneStep("ok"))
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read), Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Steps[1].Err, "panicked") || !strings.Contains(res.Steps[1].Err, "index out of range") {
		t.Errorf("Steps[1].Err = %q, want the recovered panic", res.Steps[1].Err)
	}
	if len(res.Visited) != 0 {
		t.Errorf("Visited = %v, want empty", res.Visited)
	}
}

func TestBootstrapDegradesWhenRepoTreeMissing(t *testing.T) {
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "package a\n", Meta: map[string]any{"path": args["path"]}}, nil
	})
	dec := script(readStep("a.go", "guess"), doneStep("ok"))
	var trace bytes.Buffer
	a, _ := New(Options{Root: "/r", Registry: newRegistry(read), Decider: dec, Budget: 5, Trace: &trace})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Steps) == 0 || res.Steps[0].Call.Name != contracts.ToolRepoTree {
		t.Fatalf("bootstrap step missing: %+v", res.Steps)
	}
	if !strings.Contains(res.Steps[0].Err, "not registered") {
		t.Errorf("Steps[0].Err = %q", res.Steps[0].Err)
	}
	if want := []string{"a.go"}; !reflect.DeepEqual(res.Visited, want) {
		t.Errorf("Visited = %v, want %v - the run must continue without a tree", res.Visited, want)
	}
	// With no follow_imports tool registered, auto-follow is simply skipped.
	for _, s := range res.Steps {
		if s.Call.Name == contracts.ToolFollowImports {
			t.Errorf("unexpected follow_imports step: %+v", s)
		}
	}
}

func TestRereadIsSubstituted(t *testing.T) {
	tree := treeTool("tree", contracts.RepoFile{Path: "a.go"}, contracts.RepoFile{Path: "b.go"})
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		p, _ := args["path"].(string)
		return contracts.ToolResult{Text: "package x\n", Meta: map[string]any{"path": p}}, nil
	})
	var trace bytes.Buffer
	dec := script(
		readStep("a.go", "start here"),
		readStep("a.go", "read it again"),
		doneStep("ok"),
	)
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read), Decider: dec, Budget: 10, Trace: &trace})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := []string{"a.go", "b.go"}; !reflect.DeepEqual(res.Visited, want) {
		t.Fatalf("Visited = %v, want %v", res.Visited, want)
	}
	if !anyReason(res.Decisions, "already read a.go") {
		t.Errorf("no decision records the substitution: %+v", res.Decisions)
	}
	if !anyRefusal(res.Steps, "already read a.go") {
		t.Errorf("no step records the refusal back to the model: %+v", res.Steps)
	}
	if !strings.Contains(trace.String(), "already read") {
		t.Errorf("trace should mention the substitution:\n%s", trace.String())
	}
}

func TestRereadWithNothingLeftStopsCleanly(t *testing.T) {
	tree := treeTool("tree", contracts.RepoFile{Path: "a.go"})
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "package a\n", Meta: map[string]any{"path": args["path"]}}, nil
	})
	dec := &scriptDecider{steps: []scriptStep{readStep("a.go", "again and again")}, loop: true}
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read), Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StoppedBecause != "no progress" {
		t.Errorf("StoppedBecause = %q, want \"no progress\"", res.StoppedBecause)
	}
	if res.StepsUsed != 2 {
		t.Errorf("StepsUsed = %d, want 2 (one real read, then nothing left to do)", res.StepsUsed)
	}
}

// ---------------------------------------------------------------------------
// result folding
// ---------------------------------------------------------------------------

func TestNilMetaDoesNotPanic(t *testing.T) {
	tree := tool(contracts.ToolRepoTree, func(map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "tree"}, nil // nil Meta
	})
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "class Thing:\n    pass\n"}, nil // nil Meta
	})
	follow := tool(contracts.ToolFollowImports, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "none"}, nil // nil Meta
	})
	list := tool(contracts.ToolListDir, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "-"}, nil
	})
	search := tool(contracts.ToolSearch, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "-"}, nil
	})
	dec := script(
		readStep("pkg/thing.py", "only candidate"),
		scriptStep{dec: contracts.Decision{Tool: contracts.ToolListDir, Args: map[string]any{"path": "pkg"}, Reason: "look"}},
		scriptStep{dec: contracts.Decision{Tool: contracts.ToolSearch, Args: map[string]any{"pattern": "Thing"}, Reason: "grep"}},
		doneStep("ok"),
	)
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read, follow, list, search), Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The path came from the request args because Meta carried nothing.
	if want := []string{"pkg/thing.py"}; !reflect.DeepEqual(res.Visited, want) {
		t.Errorf("Visited = %v, want %v", res.Visited, want)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Note, "class Thing") {
		t.Errorf("Findings = %+v", res.Findings)
	}
	if want := []string{"Thing"}; !reflect.DeepEqual(res.Searches, want) {
		t.Errorf("Searches = %v, want %v", res.Searches, want)
	}
	if res.Imports == nil || res.RawImports == nil {
		t.Error("maps must never be nil")
	}
}

func TestMetaCoercionFromJSONShapes(t *testing.T) {
	// Everything below is the shape Meta takes after a JSON round trip:
	// []any of map[string]any, []any of string, float64 for ints.
	tree := tool(contracts.ToolRepoTree, func(map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{
			Text: "tree",
			Meta: map[string]any{
				"files": []any{
					map[string]any{"path": "svc/main.go", "lang": "go", "size": float64(120)},
					map[string]any{"path": "svc/util.go", "lang": "go", "size": float64(60)},
					"svc/extra.go",
				},
				"count": float64(3),
			},
		}, nil
	})
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{
			Text: "package svc\n",
			Meta: map[string]any{
				"path":      "svc/main.go",
				"lang":      "go",
				"size":      float64(999),
				"truncated": "true",
				"imports":   []any{"fmt", "./util"},
			},
		}, nil
	})
	follow := tool(contracts.ToolFollowImports, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{
			Text: "ok",
			Meta: map[string]any{
				"imports":  []any{"fmt", "./util"},
				"resolved": []any{"svc/util.go", "svc/util.go", 17},
			},
		}, nil
	})
	dec := script(readStep("svc/main.go", "entry"), doneStep("ok"))
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read, follow), Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Files) != 3 {
		t.Fatalf("Files = %+v, want 3", res.Files)
	}
	if res.Files[1].Size != 60 || res.Files[1].Lang != "go" {
		t.Errorf("Files[1] = %+v, want size 60 from float64", res.Files[1])
	}
	if got := res.Files[0]; got.Path != "svc/main.go" || got.Size != 999 {
		t.Errorf("Files[0] = %+v, want size 999 from the read", got)
	}
	if want := []string{"fmt", "./util"}; !reflect.DeepEqual(res.RawImports["svc/main.go"], want) {
		t.Errorf("RawImports = %v, want %v", res.RawImports["svc/main.go"], want)
	}
	if want := []string{"svc/util.go"}; !reflect.DeepEqual(res.Imports["svc/main.go"], want) {
		t.Errorf("Imports = %v, want %v (deduped, non-strings dropped)", res.Imports["svc/main.go"], want)
	}
	if !strings.Contains(res.Findings[0].Note, "truncated") {
		t.Errorf("Finding note = %q, want the truncation flag coerced from \"true\"", res.Findings[0].Note)
	}
}

func TestUnsafePathsAreRejected(t *testing.T) {
	tree := treeTool("tree", contracts.RepoFile{Path: "a.go"},
		contracts.RepoFile{Path: "../outside.go"}, // dropped from the inventory
		contracts.RepoFile{Path: "/etc/passwd"},   // dropped from the inventory
		contracts.RepoFile{Path: "./nested/b.go"}, // normalised to nested/b.go
	)
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		// A tool that reports a hostile path: the agent must fall back to the
		// path it actually asked for.
		return contracts.ToolResult{
			Text: "package a\n",
			Meta: map[string]any{"path": "/etc/shadow", "imports": []string{"x"}},
		}, nil
	})
	follow := tool(contracts.ToolFollowImports, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Meta: map[string]any{"resolved": []string{"../escape.go", "nested/b.go"}}}, nil
	})
	dec := script(
		scriptStep{dec: contracts.Decision{Tool: contracts.ToolReadFile, Args: map[string]any{"path": "../../etc/passwd"}, Reason: "hallucination"}},
		doneStep("ok"),
	)
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read, follow), Decider: dec, Budget: 10})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, f := range res.Files {
		if strings.Contains(f.Path, "..") || strings.HasPrefix(f.Path, "/") {
			t.Errorf("unsafe path leaked into Files: %q", f.Path)
		}
	}
	if len(res.Files) != 2 {
		t.Errorf("Files = %+v, want only a.go and nested/b.go", res.Files)
	}
	// The hostile request was substituted with the top-ranked unread file.
	if want := []string{"a.go"}; !reflect.DeepEqual(res.Visited, want) {
		t.Errorf("Visited = %v, want %v", res.Visited, want)
	}
	if !strings.Contains(res.Decisions[1].Reason, "unsafe path") {
		t.Errorf("Decisions[1].Reason = %q", res.Decisions[1].Reason)
	}
	if want := []string{"nested/b.go"}; !reflect.DeepEqual(res.Imports["a.go"], want) {
		t.Errorf("Imports[a.go] = %v, want %v (the ../escape.go edge dropped)", res.Imports["a.go"], want)
	}
}

func TestTraceIsWritten(t *testing.T) {
	reg, _, _ := happyRegistry()
	var buf bytes.Buffer
	dec := script(readStep("cmd/app/main.go", "looks like the entrypoint"), doneStep("mapped it"))
	a, _ := New(Options{Root: "/r", Registry: reg, Decider: dec, Budget: 25, Trace: &buf})
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"step 0/25  repo_tree{}  -> 2 files",
		"step 1/25  read_file{path:cmd/app/main.go}  -> 412 B, 1 imports",
		"(reason: looks like the entrypoint)",
		"step 1/25  follow_imports{path:cmd/app/main.go}  -> 1 imports, 1 resolved",
		"stopped: model signalled done",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("trace missing %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			t.Error("trace contains a blank line")
		}
	}
}

func TestNoTraceWriterIsSilent(t *testing.T) {
	reg, _, _ := happyRegistry()
	dec := script(readStep("cmd/app/main.go", "go"), doneStep("ok"))
	a, _ := New(Options{Root: "/r", Registry: reg, Decider: dec})
	if _, err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run with a nil Trace must not fail: %v", err)
	}
}

// ---------------------------------------------------------------------------
// transcript bound
// ---------------------------------------------------------------------------

func TestTranscriptIsBounded(t *testing.T) {
	files := make([]contracts.RepoFile, 0, 12)
	for i := 0; i < 12; i++ {
		files = append(files, contracts.RepoFile{Path: fmt.Sprintf("f%02d.go", i)})
	}
	tree := treeTool("tree", files...)
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: "package f\n", Meta: map[string]any{"path": args["path"]}}, nil
	})
	brain := &funcDecider{fn: func(_ context.Context, obs contracts.Observation) (contracts.Decision, error) {
		if len(obs.Transcript) > maxTranscript {
			return contracts.Decision{}, fmt.Errorf("transcript of %d exceeds cap %d", len(obs.Transcript), maxTranscript)
		}
		if len(obs.Unread) == 0 {
			return contracts.Decision{Done: true, Reason: "all read"}, nil
		}
		return contracts.Decision{Tool: contracts.ToolReadFile, Args: map[string]any{"path": obs.Unread[0]}, Reason: "next"}, nil
	}}
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree, read), Decider: brain, Budget: 12})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Visited) != 12 {
		t.Errorf("Visited %d files, want 12", len(res.Visited))
	}
	for _, s := range res.Steps {
		if s.Err != "" {
			t.Fatalf("unexpected error step: %+v", s)
		}
	}
}

func TestResultTextIsTruncated(t *testing.T) {
	big := strings.Repeat("x", maxResultText*3)
	tree := tool(contracts.ToolRepoTree, func(map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Text: big, Meta: map[string]any{"files": []contracts.RepoFile{{Path: "a.go"}}}}, nil
	})
	dec := script(doneStep("ok"))
	a, _ := New(Options{Root: "/r", Registry: newRegistry(tree), Decider: dec, Budget: 3})
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Steps[0].Result) != maxResultText {
		t.Errorf("stored result is %d chars, want %d", len(res.Steps[0].Result), maxResultText)
	}
	// The full tree text is still what the model sees in the observation.
	if a.tree != big {
		t.Error("the tree snapshot must not be truncated")
	}
}

// TestRepeatedCallIsRepaired encodes a failure seen with the real local model:
// qwen2.5:1.5b answered {"tool":"repo_tree"} on every single step ("understand
// the directory structure"), so the whole budget went on re-reading the tree
// the agent had already put in front of it, and the run ended with zero files
// read. The agent now overrides a question it has already answered and opens
// the next unread file instead, so the run still produces a useful map.
func TestRepeatedCallIsRepaired(t *testing.T) {
	tree := treeTool("a.go\nb.go", contracts.RepoFile{Path: "a.go"}, contracts.RepoFile{Path: "b.go"})
	read := tool(contracts.ToolReadFile, func(args map[string]any) (contracts.ToolResult, error) {
		p, _ := args["path"].(string)
		return contracts.ToolResult{
			Text: "package main",
			Meta: map[string]any{"path": p, "lang": "go", "size": 12},
		}, nil
	})
	follow := tool(contracts.ToolFollowImports, func(args map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{Meta: map[string]any{"imports": []string{}, "resolved": []string{}}}, nil
	})
	// A brain stuck in a groove: always repo_tree, never anything else.
	stuck := &funcDecider{fn: func(_ context.Context, _ contracts.Observation) (contracts.Decision, error) {
		return contracts.Decision{Tool: contracts.ToolRepoTree, Args: map[string]any{}, Reason: "understand the directory structure"}, nil
	}}
	var trace strings.Builder
	a, err := New(Options{
		Root: "/r", Registry: newRegistry(tree, read, follow),
		Decider: stuck, Budget: 6, Trace: &trace,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	res, err := a.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Despite the model never once asking for a file, both files get read.
	if want := []string{"a.go", "b.go"}; !reflect.DeepEqual(res.Visited, want) {
		t.Errorf("Visited = %v, want %v (repeats must be repaired into reads)", res.Visited, want)
	}
	if !strings.Contains(trace.String(), "repeat of repo_tree") {
		t.Errorf("trace does not record the substitution:\n%s", trace.String())
	}
	// repo_tree must have actually run exactly once: the bootstrap. Every later
	// request for it is recorded as a refusal (non-empty Err), never dispatched.
	dispatched := 0
	for _, st := range res.Steps {
		if st.Call.Name == contracts.ToolRepoTree && st.Err == "" {
			dispatched++
		}
	}
	if dispatched != 1 {
		t.Errorf("repo_tree dispatched %d times, want 1 (the bootstrap)", dispatched)
	}
	if !anyRefusal(res.Steps, "already called") {
		t.Errorf("the repeat was not reported back to the model: %+v", res.Steps)
	}
}

// anyReason reports whether any decision's Reason contains want.
func anyReason(ds []contracts.Decision, want string) bool {
	for _, d := range ds {
		if strings.Contains(d.Reason, want) {
			return true
		}
	}
	return false
}

// anyRefusal reports whether any exchange carries a refusal mentioning want.
func anyRefusal(steps []contracts.Exchange, want string) bool {
	for _, st := range steps {
		if strings.Contains(st.Err, "REFUSED") && strings.Contains(st.Err, want) {
			return true
		}
	}
	return false
}
