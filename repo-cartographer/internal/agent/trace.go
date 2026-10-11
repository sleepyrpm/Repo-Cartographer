package agent

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// Human-readable tracing. One tidy line per step, e.g.
//
//	step 0/25  repo_tree{}  -> 41 files   (reason: bootstrap: map the repository ...)
//	step 1/25  read_file{path:cmd/cartographer/main.go}  -> 412 B, 3 imports   (reason: looks like the entrypoint)
//	step 1/25  follow_imports{path:cmd/cartographer/main.go}  -> 3 imports, 2 resolved   (reason: agent auto-follow ...)
//	stopped: model signalled done (steps used 7/25, 6 files read, 11 import edges)
//
// Step 0 is the bootstrap call; agent-initiated auto-follow calls share the
// number of the step that triggered them. Nothing is ever written to stdout:
// the only sink is Options.Trace.

func (a *Agent) tracef(format string, args ...any) {
	if a.opts.Trace == nil {
		return
	}
	fmt.Fprintf(a.opts.Trace, format, args...)
}

func (a *Agent) traceStep(step int, dec contracts.Decision, ex contracts.Exchange, res contracts.ToolResult) {
	if a.opts.Trace == nil {
		return
	}
	a.tracef("step %d/%d  %s%s  -> %s   (reason: %s)\n",
		step, a.budget,
		dec.Tool, briefArgs(dec.Args),
		outcome(dec.Tool, ex, res),
		reasonOf(dec.Reason),
	)
}

// briefArgs renders a decision's args compactly and deterministically.
func briefArgs(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%s", k, clip(oneLine(fmt.Sprint(args[k])), 60)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// outcome summarises what a tool gave back, per tool, for the trace line.
func outcome(name string, ex contracts.Exchange, res contracts.ToolResult) string {
	if ex.Err != "" {
		return "error: " + clip(oneLine(ex.Err), 120)
	}
	switch name {
	case contracts.ToolRepoTree:
		n := asInt(metaGet(res.Meta, "count"))
		if n == 0 {
			n = len(asRepoFiles(metaGet(res.Meta, "files")))
		}
		return fmt.Sprintf("%d files", n)
	case contracts.ToolReadFile:
		size := asInt(metaGet(res.Meta, "size"))
		if size == 0 {
			size = len(res.Text)
		}
		s := fmt.Sprintf("%d B, %d imports", size, len(asStrings(metaGet(res.Meta, "imports"))))
		if asBool(metaGet(res.Meta, "truncated")) {
			s += " (truncated)"
		}
		return s
	case contracts.ToolFollowImports:
		return fmt.Sprintf("%d imports, %d resolved",
			len(asStrings(metaGet(res.Meta, "imports"))),
			len(asStrings(metaGet(res.Meta, "resolved"))))
	case contracts.ToolSearch:
		n := asInt(metaGet(res.Meta, "count"))
		if n == 0 {
			n = len(asStrings(metaGet(res.Meta, "matches")))
		}
		return fmt.Sprintf("%d matches", n)
	case contracts.ToolListDir:
		return fmt.Sprintf("%d entries", len(asStrings(metaGet(res.Meta, "entries"))))
	default:
		return fmt.Sprintf("%d B", len(res.Text))
	}
}
