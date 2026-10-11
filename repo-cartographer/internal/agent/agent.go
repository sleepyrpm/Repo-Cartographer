package agent

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// DefaultBudget is the number of model decisions a run is allowed when
// Options.Budget is left at zero.
const DefaultBudget = 25

// Tuning knobs. These are deliberately small: the prompt handed to a 1.5B
// local model has to stay short enough to be answered sensibly, and the Result
// has to stay printable.
const (
	maxTranscript  = 9    // exchanges shown back to the model
	maxUnread      = 25   // candidate files offered to the model
	maxResultText  = 1200 // chars of tool output kept per Exchange
	maxFindingNote = 160  // chars of a Finding note
	stallLimit     = 3    // consecutive unproductive steps before giving up
)

// Canonical StoppedBecause values.
const (
	stopDone       = "model signalled done"
	stopBudget     = "step budget exhausted"
	stopNoProgress = "no progress"
	stopCancelled  = "context cancelled"
)

const bootstrapReason = "bootstrap: map the repository before the first model decision"

// Options configures a run. Root, Registry and Decider are required.
type Options struct {
	Root     string             // repo root (already validated by the caller)
	Registry contracts.Registry // required: the toolbox the agent acts with
	Decider  contracts.Decider  // required: the brain that picks the next tool
	Budget   int                // max decide steps; <=0 means DefaultBudget
	Trace    io.Writer          // optional; one human-readable line per step
	Clock    func() time.Time   // optional, for deterministic tests
}

// Result is everything the run learned. Every slice is in the order it was
// produced, every map is non-nil, and nothing in here is a summary: rendering
// is internal/report's job.
type Result struct {
	// RootPath is the repository root the run explored.
	RootPath string
	// Steps is the full act-by-act trace, in order. It includes the implicit
	// bootstrap repo_tree call and the implicit follow_imports calls the agent
	// makes after every successful read, so it is a complete audit log.
	Steps []contracts.Exchange
	// Decisions is what was chosen at each step, strictly parallel to Steps
	// (len(Decisions) == len(Steps)). For agent-initiated steps (bootstrap,
	// auto-follow) and for substituted steps the Reason field says so. A final
	// Done decision runs no tool and therefore adds no entry here; it is
	// reflected in StoppedBecause instead.
	Decisions []contracts.Decision
	// Visited lists the files actually read, in read order, deduped.
	Visited []string
	// Files is the discovered file inventory: seeded from repo_tree's
	// Meta["files"] and grown by reads, searches and directory listings.
	Files []contracts.RepoFile
	// Imports holds the resolved dependency edges: repo-relative file ->
	// repo-relative files inside this repo that it imports. Never nil.
	Imports map[string][]string
	// RawImports holds the unresolved import specs per file, exactly as the
	// tools reported them (e.g. "encoding/json", "./util"). Never nil.
	RawImports map[string][]string
	// Findings are one-line notes gathered while reading files.
	Findings []contracts.Finding
	// Searches are the search patterns the agent tried, in order, deduped.
	Searches []string
	// StepsUsed is how many model decisions were consumed (bootstrap and
	// auto-follow calls are free and are not counted).
	StepsUsed int
	// StoppedBecause is one of "model signalled done", "step budget
	// exhausted", "no progress", or "context cancelled: <err>".
	StoppedBecause string
	// Duration is wall-clock time of the run, measured with Options.Clock.
	Duration time.Duration
}

// Agent runs the observe -> decide -> act -> record loop.
type Agent struct {
	opts   Options
	budget int

	res *Result

	// derived indexes, kept alongside Result so folding stays O(1)
	visited  map[string]bool
	fileAt   map[string]int             // path -> index into res.Files
	searched map[string]bool            //
	called   map[string]bool            // tool calls already made, by signature
	internal []bool                     // parallel to res.Steps: was this step the agent's own?
	edges    map[string]map[string]bool // dedupe import edges
	tree     string                     // latest repo tree text
}

// New validates the options and returns a ready agent.
func New(opts Options) (*Agent, error) {
	if opts.Root == "" {
		return nil, errors.New("agent: Options.Root must not be empty")
	}
	if opts.Registry == nil {
		return nil, errors.New("agent: Options.Registry must not be nil")
	}
	if opts.Decider == nil {
		return nil, errors.New("agent: Options.Decider must not be nil")
	}
	budget := opts.Budget
	if budget <= 0 {
		budget = DefaultBudget
	}
	if opts.Clock == nil {
		opts.Clock = time.Now
	}
	return &Agent{
		opts:   opts,
		budget: budget,
		res: &Result{
			RootPath:   opts.Root,
			Imports:    map[string][]string{},
			RawImports: map[string][]string{},
		},
		visited:  map[string]bool{},
		fileAt:   map[string]int{},
		searched: map[string]bool{},
		called:   map[string]bool{},
		edges:    map[string]map[string]bool{},
	}, nil
}

// Run drives the loop until the model says Done, the step budget runs out, the
// agent stops making progress, or the context is cancelled.
//
// Run always returns a non-nil *Result - even alongside an error - because a
// partial map of the repository is still worth printing.
func (a *Agent) Run(ctx context.Context) (*Result, error) {
	start := a.opts.Clock()
	defer func() { a.res.Duration = a.opts.Clock().Sub(start) }()

	// ---------------------------------------------------------------- 0. BOOTSTRAP
	// The model is useless without a map, and repo_tree is cheap, so the agent
	// spends no decision on it: it calls it itself, seeds the file inventory
	// and records the call as a normal step so the trace stays complete.
	a.bootstrap()

	stall := 0 // consecutive steps that taught us nothing new

	for {
		// ------------------------------------------------------------ STOP CHECKS
		if a.res.StepsUsed >= a.budget {
			a.stop(stopBudget)
			return a.res, nil
		}
		if err := ctx.Err(); err != nil {
			a.stop(stopCancelled + ": " + err.Error())
			return a.res, err
		}

		step := a.res.StepsUsed + 1

		// ------------------------------------------------------------ 1. OBSERVE
		obs := a.observe(step)

		// ------------------------------------------------------------ 2. DECIDE
		dec, derr := a.opts.Decider.Decide(ctx, obs)
		a.res.StepsUsed = step // the question was asked; the step is spent

		if derr != nil {
			// A broken brain is not a broken agent. Log it and fall back to the
			// obvious next move: open the most promising unread file.
			// (internal/model already returns a fallback decision with a nil
			// error, so this is belt-and-braces.)
			a.tracef("step %d/%d  decider error: %s\n", step, a.budget, oneLine(clip(derr.Error(), 160)))
			sub, ok := a.safeDefault("decider error: " + oneLine(clip(derr.Error(), 80)))
			if !ok {
				a.stop(stopNoProgress)
				return a.res, nil
			}
			dec = sub
		}

		// The decider may have been interrupted rather than failed.
		if err := ctx.Err(); err != nil {
			a.stop(stopCancelled + ": " + err.Error())
			return a.res, err
		}

		// --------------------------------------------------- 3. ACT (or stop)
		if dec.Done {
			a.tracef("step %d/%d  done   (reason: %s)\n", step, a.budget, reasonOf(dec.Reason))
			a.stop(stopDone)
			return a.res, nil
		}

		dec, ok := a.vet(step, dec)
		if !ok {
			// Nothing sensible left to do (unknown tool / bad path / re-read and
			// no unread files remain).
			a.stop(stopNoProgress)
			return a.res, nil
		}

		// ------------------------------------------------- 3. ACT + 4. RECORD
		// perform() dispatches the tool, folds its ToolResult into the
		// Result's knowledge of the repo, appends the Exchange+Decision and
		// (after a successful read) auto-follows that file's imports.
		before := a.progress()
		a.perform(step, dec)

		// ------------------------------------------------------------ 5. STOP: STALL
		// A small model will happily list_dir "." forever. If three steps in a
		// row add no file, no edge and no search, we are done learning.
		if a.progress() == before {
			stall++
			if stall >= stallLimit {
				a.tracef("step %d/%d  no new information for %d consecutive steps\n", step, a.budget, stall)
				a.stop(stopNoProgress)
				return a.res, nil
			}
		} else {
			stall = 0
		}
	}
}

// ---------------------------------------------------------------------------
// bootstrap
// ---------------------------------------------------------------------------

// bootstrap calls repo_tree once, before any model decision, to seed the file
// inventory and the tree snapshot. Missing or failing repo_tree is survivable:
// the model simply starts with an empty map.
func (a *Agent) bootstrap() {
	args := map[string]any{}
	dec := contracts.Decision{Tool: contracts.ToolRepoTree, Args: args, Reason: bootstrapReason}
	call := contracts.ToolCall{Name: contracts.ToolRepoTree, Args: args}

	tool, found := a.opts.Registry.Get(contracts.ToolRepoTree)
	if !found {
		ex := contracts.Exchange{Call: call, Err: "tool " + contracts.ToolRepoTree + " is not registered"}
		a.recordInternal(dec, ex)
		a.traceStep(0, dec, ex, contracts.ToolResult{})
		return
	}

	res, err := a.callTool(tool, args)
	ex := contracts.Exchange{Call: call, Result: clip(res.Text, maxResultText)}
	if err != nil {
		ex.Err = err.Error()
	} else {
		a.tree = res.Text
		a.foldRepoTree(res)
	}
	a.recordInternal(dec, ex)
	a.traceStep(0, dec, ex, res)
}

// ---------------------------------------------------------------------------
// 1. OBSERVE
// ---------------------------------------------------------------------------

// observe packs the current state of the investigation into the Observation the
// model sees. Everything in it is bounded so the prompt stays small.
func (a *Agent) observe(step int) contracts.Observation {
	return contracts.Observation{
		RootPath:   a.res.RootPath,
		Step:       step,
		Budget:     a.budget,
		Tools:      a.opts.Registry.Describe(),
		Tree:       a.tree,
		Visited:    append([]string(nil), a.res.Visited...),
		Unread:     a.unread(maxUnread),
		Transcript: a.modelTranscript(maxTranscript),
	}
}

// modelTranscript is the last n exchanges the MODEL is responsible for: its own
// tool calls and the agent's refusals of them. The agent's own steps - the
// bootstrap repo_tree and the auto-follow after every read - are deliberately
// left out.
//
// This is not cosmetic. A 1.5B model copies the shape of the most recent line
// it is shown, so when the transcript ended with the agent's own
// follow_imports{path:X} the model's next answer was follow_imports{path:X}
// again, step after step. Showing it only its own moves removes the echo.
// Those steps are still in Result.Steps, so the audit log stays complete.
func (a *Agent) modelTranscript(n int) []contracts.Exchange {
	out := make([]contracts.Exchange, 0, n)
	for i := len(a.res.Steps) - 1; i >= 0 && len(out) < n; i-- {
		if i < len(a.internal) && a.internal[i] {
			continue
		}
		out = append(out, a.res.Steps[i])
	}
	// collected newest-first; the prompt reads oldest-first
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

// ---------------------------------------------------------------------------
// 3. ACT (vetting + dispatch)
// ---------------------------------------------------------------------------

// vet sanity-checks a decision before it is dispatched and, where it can,
// repairs it instead of wasting the step:
//
//   - unknown tool name            -> substitute the safe default
//   - read_file with a bad path    -> substitute the safe default
//   - read_file of a visited file  -> substitute the safe default (skip-and-nudge)
//   - an exact repeat of an earlier call -> substitute the safe default
//
// The repeat check matters more than it looks: a small local model that likes
// its own last answer will happily ask for repo_tree (or list_dir ".") on
// every single step, and without this the whole budget is spent relearning
// the same fact. Re-asking a question the agent has already answered is never
// progress, so the agent overrides it and opens the next unread file instead.
//
// The substitution is recorded in the Decision's Reason so the trace and the
// report show exactly what the agent did and why.
func (a *Agent) vet(step int, dec contracts.Decision) (contracts.Decision, bool) {
	if _, found := a.opts.Registry.Get(dec.Tool); !found {
		a.tracef("step %d/%d  unknown tool %q - substituting\n", step, a.budget, dec.Tool)
		why := "model asked for unknown tool " + quote(dec.Tool)
		sub, ok := a.safeDefault(why)
		if !ok {
			return contracts.Decision{}, false
		}
		return a.refuse(step, dec, why, sub), true
	}

	if dec.Tool == contracts.ToolReadFile {
		want := argString(dec.Args, "path")
		norm, ok := normFile(want)
		if !ok {
			a.tracef("step %d/%d  rejected path %q - substituting\n", step, a.budget, want)
			return a.safeDefault("rejected unsafe path " + quote(want))
		}
		if a.visited[norm] {
			a.tracef("step %d/%d  %s already read - substituting\n", step, a.budget, norm)
			why := "already read " + norm
			sub, ok := a.safeDefault(why)
			if !ok {
				return contracts.Decision{}, false
			}
			return a.refuse(step, dec, why, sub), true
		}
		// Hand the tool the normalised path, keeping any extra args.
		dec.Args = withArg(dec.Args, "path", norm)
	}

	// Repeated identical call: the answer is already in the transcript.
	if sig := callSignature(dec.Tool, dec.Args); a.called[sig] {
		a.tracef("step %d/%d  repeat of %s - substituting\n", step, a.budget, sig)
		why := "already called " + sig + " earlier in this run"
		sub, ok := a.safeDefault(why)
		if !ok {
			return contracts.Decision{}, false
		}
		return a.refuse(step, dec, why, sub), true
	}
	return dec, true
}

// refuse records the model's rejected call as an Exchange carrying the reason
// it was overridden, then returns the substitute.
//
// Recording the refusal is what closes the feedback loop. Without it the
// rejected call simply vanishes: the transcript shows only the substituted
// read, so the model never learns its choice was overridden and asks for the
// very same thing again next step. Observed with qwen2.5:1.5b, which repeated
// one follow_imports call for seven consecutive steps. The refusal lands in
// Exchange.Err, which is exactly how the prompt already surfaces tool errors.
func (a *Agent) refuse(step int, rejected contracts.Decision, why string, sub contracts.Decision) contracts.Decision {
	a.record(rejected, contracts.Exchange{
		Call: contracts.ToolCall{Name: rejected.Tool, Args: rejected.Args},
		Err:  "REFUSED by the agent: " + why + ". It read " + argString(sub.Args, "path") + " instead. Choose something different.",
	})
	_ = step
	return sub
}

// safeDefault is the agent's own opinion of what to do next when the model's
// answer is unusable: read the highest-ranked file nobody has opened yet.
func (a *Agent) safeDefault(why string) (contracts.Decision, bool) {
	if _, found := a.opts.Registry.Get(contracts.ToolReadFile); !found {
		return contracts.Decision{}, false
	}
	next := a.unread(1)
	if len(next) == 0 {
		return contracts.Decision{}, false
	}
	return contracts.Decision{
		Tool:   contracts.ToolReadFile,
		Args:   map[string]any{"path": next[0]},
		Reason: "agent fallback (" + why + "): reading next unread file " + next[0],
	}, true
}

// perform dispatches one vetted decision, folds the result into state and
// records the exchange. A tool error is not fatal: it lands in Exchange.Err so
// the model can read it and adapt on the next step.
func (a *Agent) perform(step int, dec contracts.Decision) {
	tool, found := a.opts.Registry.Get(dec.Tool)
	if !found { // vet() already checked; belt-and-braces
		ex := contracts.Exchange{
			Call: contracts.ToolCall{Name: dec.Tool, Args: dec.Args},
			Err:  "tool " + dec.Tool + " is not registered",
		}
		a.record(dec, ex)
		a.traceStep(step, dec, ex, contracts.ToolResult{})
		return
	}

	call := contracts.ToolCall{Name: dec.Tool, Args: dec.Args}
	res, err := a.callTool(tool, dec.Args)
	ex := contracts.Exchange{Call: call, Result: clip(res.Text, maxResultText)}
	if err != nil {
		ex.Err = err.Error()
	}

	// ------------------------------------------------------------- 4. RECORD
	readPath, didRead := "", false
	if err == nil {
		readPath, didRead = a.fold(dec, res)
	}
	a.record(dec, ex)
	a.traceStep(step, dec, ex, res)

	// Auto-follow: every file we read gets its imports resolved immediately.
	// This is the single cheapest way to grow the dependency graph, and the
	// model never has to think of it.
	if didRead {
		a.autoFollow(step, readPath)
	}
}

// autoFollow issues an implicit follow_imports call for a file the agent just
// read. It is recorded as a normal step (so the trace is honest) but costs no
// decision from the budget.
func (a *Agent) autoFollow(step int, path string) {
	tool, found := a.opts.Registry.Get(contracts.ToolFollowImports)
	if !found {
		return
	}
	args := map[string]any{"path": path}
	dec := contracts.Decision{
		Tool:   contracts.ToolFollowImports,
		Args:   args,
		Reason: "agent auto-follow: resolve the imports of " + path,
	}
	res, err := a.callTool(tool, args)
	ex := contracts.Exchange{
		Call:   contracts.ToolCall{Name: contracts.ToolFollowImports, Args: args},
		Result: clip(res.Text, maxResultText),
	}
	if err != nil {
		ex.Err = err.Error()
	} else {
		a.foldFollowImports(path, res)
	}
	a.recordInternal(dec, ex)
	a.traceStep(step, dec, ex, res)
}

// callTool invokes a tool with a recover() barrier: a third-party tool that
// panics becomes an ordinary Exchange.Err, never a crashed demo.
func (a *Agent) callTool(tool contracts.Tool, args map[string]any) (res contracts.ToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			res = contracts.ToolResult{}
			err = &panicError{tool: tool.Name(), value: r}
		}
	}()
	return tool.Call(args)
}

// ---------------------------------------------------------------------------
// bookkeeping
// ---------------------------------------------------------------------------

// record appends one step to the trace, keeping Steps and Decisions parallel.
func (a *Agent) record(dec contracts.Decision, ex contracts.Exchange) {
	a.recordAs(dec, ex, false)
}

// recordInternal records a step the agent took on its own initiative (the
// bootstrap tree, an auto-follow). These are kept in the audit log but hidden
// from the model's transcript - see observe().
func (a *Agent) recordInternal(dec contracts.Decision, ex contracts.Exchange) {
	a.recordAs(dec, ex, true)
}

func (a *Agent) recordAs(dec contracts.Decision, ex contracts.Exchange, own bool) {
	a.res.Steps = append(a.res.Steps, ex)
	a.res.Decisions = append(a.res.Decisions, dec)
	a.internal = append(a.internal, own)
	// Remember the call so vet() can refuse to ask the same question twice.
	// The bootstrap repo_tree is recorded here too, which is what stops a
	// model from burning step 1 re-requesting the tree it was just shown.
	a.called[callSignature(ex.Call.Name, ex.Call.Args)] = true
}

// progress is the agent's "have I learned anything?" fingerprint: files read,
// import edges discovered, searches performed. Used by the no-progress guard.
func (a *Agent) progress() [3]int {
	edges := 0
	for _, to := range a.edges {
		edges += len(to)
	}
	return [3]int{len(a.res.Visited), edges, len(a.res.Searches)}
}

func (a *Agent) stop(reason string) {
	a.res.StoppedBecause = reason
	a.tracef("stopped: %s (steps used %d/%d, %d files read, %d import edges)\n",
		reason, a.res.StepsUsed, a.budget, len(a.res.Visited), a.progress()[1])
}
