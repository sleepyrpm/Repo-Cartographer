package agent

import (
	"context"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// ---------------------------------------------------------------------------
// Test doubles. The agent package depends on nothing but internal/contracts,
// so every collaborator below is a local stub: no network, no filesystem, no
// other package of this module.
// ---------------------------------------------------------------------------

type stubTool struct {
	name  string
	desc  string
	fn    func(args map[string]any) (contracts.ToolResult, error)
	calls []map[string]any
}

func (t *stubTool) Name() string        { return t.name }
func (t *stubTool) Description() string { return t.desc }
func (t *stubTool) Call(args map[string]any) (contracts.ToolResult, error) {
	t.calls = append(t.calls, args)
	if t.fn == nil {
		return contracts.ToolResult{}, nil
	}
	return t.fn(args)
}

func tool(name string, fn func(args map[string]any) (contracts.ToolResult, error)) *stubTool {
	return &stubTool{name: name, desc: name + " (stub)", fn: fn}
}

type stubRegistry struct {
	order  []contracts.Tool
	byName map[string]contracts.Tool
}

func newRegistry(ts ...contracts.Tool) *stubRegistry {
	r := &stubRegistry{byName: map[string]contracts.Tool{}}
	for _, t := range ts {
		r.order = append(r.order, t)
		r.byName[t.Name()] = t
	}
	return r
}

func (r *stubRegistry) Get(name string) (contracts.Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}
func (r *stubRegistry) List() []contracts.Tool { return r.order }
func (r *stubRegistry) Describe() []contracts.ToolInfo {
	out := make([]contracts.ToolInfo, 0, len(r.order))
	for _, t := range r.order {
		out = append(out, contracts.ToolInfo{Name: t.Name(), Description: t.Description()})
	}
	return out
}

// scriptDecider replays a canned list of decisions, one per step, so tests read
// like a storyboard. When the script runs out it says Done (or, with loop set,
// repeats the final entry forever).
type scriptDecider struct {
	steps []scriptStep
	loop  bool
	seen  []contracts.Observation
}

type scriptStep struct {
	dec contracts.Decision
	err error
}

func script(steps ...scriptStep) *scriptDecider { return &scriptDecider{steps: steps} }

func (d *scriptDecider) Decide(_ context.Context, obs contracts.Observation) (contracts.Decision, error) {
	d.seen = append(d.seen, obs)
	i := len(d.seen) - 1
	if i >= len(d.steps) {
		if d.loop && len(d.steps) > 0 {
			s := d.steps[len(d.steps)-1]
			return s.dec, s.err
		}
		return contracts.Decision{Done: true, Reason: "script exhausted"}, nil
	}
	return d.steps[i].dec, d.steps[i].err
}

func (d *scriptDecider) calls() int { return len(d.seen) }

// funcDecider computes its decision from the observation, for loops that have
// to keep going for a whole budget.
type funcDecider struct {
	fn func(ctx context.Context, obs contracts.Observation) (contracts.Decision, error)
	n  int
}

func (d *funcDecider) Decide(ctx context.Context, obs contracts.Observation) (contracts.Decision, error) {
	d.n++
	return d.fn(ctx, obs)
}

// ---------------------------------------------------------------------------
// shorthands
// ---------------------------------------------------------------------------

func readStep(path, reason string) scriptStep {
	return scriptStep{dec: contracts.Decision{
		Tool: contracts.ToolReadFile, Args: map[string]any{"path": path}, Reason: reason,
	}}
}

func doneStep(reason string) scriptStep {
	return scriptStep{dec: contracts.Decision{Done: true, Reason: reason}}
}

// tickClock advances one second per call, so Duration is deterministic.
func tickClock() func() time.Time {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	n := 0
	return func() time.Time {
		t := base.Add(time.Duration(n) * time.Second)
		n++
		return t
	}
}

// treeTool returns a repo_tree stub over a fixed inventory.
func treeTool(text string, files ...contracts.RepoFile) *stubTool {
	return tool(contracts.ToolRepoTree, func(map[string]any) (contracts.ToolResult, error) {
		return contracts.ToolResult{
			Text: text,
			Meta: map[string]any{"files": files, "count": len(files)},
		}, nil
	})
}
