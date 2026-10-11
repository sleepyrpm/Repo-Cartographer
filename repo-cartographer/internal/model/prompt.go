package model

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// Prompt size budgets. The target is a 1.5B model running on a laptop CPU,
// where prompt length translates almost linearly into wall-clock latency, so
// every section is clipped. The assembled prompt stays comfortably under
// ~6000 characters even for a large repo.
const (
	maxTreeChars       = 1200 // repo tree snapshot
	maxToolDescChars   = 140  // per tool description
	maxTools           = 12   // tools listed
	maxUnreadListed    = 20   // unread candidates listed
	maxVisitedListed   = 12   // already-read files listed
	maxTranscriptSteps = 4    // most recent exchanges shown
	maxTranscriptChars = 300  // per exchange result
	maxArgsChars       = 120  // per exchange rendered args
	maxPathChars       = 100  // per path line
	maxCallsListed     = 6    // past calls listed back
)

// BuildPrompt renders an Observation into the decision prompt.
//
// The structure is deliberately plain and repetitive: a short job statement,
// the live tool list straight from the registry (never hardcoded), the current
// state, the recent transcript, then a hard instruction to answer with exactly
// one JSON object plus two worked examples. Small models follow examples far
// better than they follow descriptions.
func BuildPrompt(obs contracts.Observation) string {
	var b strings.Builder
	b.Grow(4096)

	b.WriteString("You are Repo Cartographer. You are exploring an unfamiliar code repository ")
	b.WriteString("to work out its architecture and which files a newcomer should read first. ")
	b.WriteString("You investigate by calling tools: at each step you choose exactly ONE tool call, ")
	b.WriteString("you are shown its result, and then you choose again. ")
	b.WriteString("Stop as soon as you understand how the project fits together.\n\n")

	root := obs.RootPath
	if root == "" {
		root = "(unknown)"
	}
	budget := obs.Budget
	if budget <= 0 {
		budget = obs.Step
	}
	fmt.Fprintf(&b, "REPO: %s\nSTEP: %d of %d\n\n", root, obs.Step, budget)

	// ---- tools, verbatim from the registry -------------------------------
	b.WriteString("TOOLS YOU MAY CALL:\n")
	if len(obs.Tools) == 0 {
		b.WriteString("- (none advertised)\n")
	}
	for i, t := range obs.Tools {
		if i == maxTools {
			fmt.Fprintf(&b, "- (+%d more)\n", len(obs.Tools)-maxTools)
			break
		}
		fmt.Fprintf(&b, "- %s: %s\n", t.Name, oneLine(truncate(t.Description, maxToolDescChars)))
	}
	b.WriteByte('\n')

	// ---- state ------------------------------------------------------------
	if tree := strings.TrimSpace(obs.Tree); tree != "" {
		// Spell out that the tree IS the repo_tree result. Without this a small
		// model reliably answers "repo_tree" on every step to "understand the
		// directory structure", never reads a file, and learns nothing.
		b.WriteString("REPO TREE (this is already the result of repo_tree - you have it, do NOT ask for it again):\n")
		b.WriteString(truncate(tree, maxTreeChars))
		b.WriteString("\n\n")
	}

	b.WriteString("FILES ALREADY READ (do NOT read these again):\n")
	writePathList(&b, obs.Visited, maxVisitedListed, "- (none yet)")
	b.WriteByte('\n')

	b.WriteString("BEST UNREAD CANDIDATES (prefer these):\n")
	writePathList(&b, obs.Unread, maxUnreadListed, "- (none left)")
	b.WriteByte('\n')

	// ---- what has already been asked ---------------------------------------
	// Listing the calls back to the model is the cheapest defence against the
	// one-track-mind failure mode; the agent loop refuses exact repeats anyway,
	// but a step spent on a refused call is still a step wasted.
	if calls := pastCalls(obs.Transcript); len(calls) > 0 {
		b.WriteString("CALLS ALREADY MADE (do NOT repeat any of these):\n")
		writePathList(&b, calls, maxCallsListed, "- (none yet)")
		b.WriteByte('\n')
	}

	// ---- short-term memory -------------------------------------------------
	if n := len(obs.Transcript); n > 0 {
		start := 0
		if n > maxTranscriptSteps {
			start = n - maxTranscriptSteps
		}
		b.WriteString("RECENT STEPS:\n")
		for i := start; i < n; i++ {
			ex := obs.Transcript[i]
			fmt.Fprintf(&b, "%d. %s(%s) -> ", i+1, ex.Call.Name, renderArgs(ex.Call.Args))
			if ex.Err != "" {
				fmt.Fprintf(&b, "ERROR: %s\n", oneLine(truncate(ex.Err, maxTranscriptChars)))
				continue
			}
			fmt.Fprintf(&b, "%s\n", oneLine(truncate(ex.Result, maxTranscriptChars)))
		}
		b.WriteByte('\n')
	}

	// ---- the output contract ----------------------------------------------
	b.WriteString(`ANSWER FORMAT - reply with ONE JSON object and NOTHING else:
{"tool":"<tool name>","args":{...},"done":false,"reason":"<short why>"}
When you have learned enough about the architecture, answer exactly:
{"tool":"","args":{},"done":true,"reason":"<short why>"}

EXAMPLES OF CORRECT ANSWERS:
{"tool":"read_file","args":{"path":"cmd/server/main.go"},"done":false,"reason":"entrypoint wires the app together"}
{"tool":"search","args":{"pattern":"func main"},"done":false,"reason":"find the entrypoints"}

RULES:
- the "tool" value MUST be one of the tool names listed above, copied exactly
- reading files is how you learn: unless you have a specific reason to do
  otherwise, answer read_file on the FIRST entry of BEST UNREAD CANDIDATES
- never repeat a call under CALLS ALREADY MADE, and never call repo_tree again
- never call read_file on a path under FILES ALREADY READ
- one object only: no prose, no explanation, no markdown fences, no extra JSON
- set "done":true early once the architecture is clear

JSON:`)

	return b.String()
}

// retryPrompt builds the single retry: the same state, with a blunt preamble
// and postscript, because the first answer was unusable. Keeping the state
// means the model does not lose its place in the investigation.
func retryPrompt(prompt string) string {
	return "Your previous answer was INVALID. Reply with ONLY a single JSON object " +
		"on one line, starting with { and ending with }. No words before or after it.\n\n" +
		prompt + "\n(Remember: ONLY the JSON object, e.g. " +
		`{"tool":"read_file","args":{"path":"README.md"},"done":false,"reason":"overview"})` + "\nJSON:"
}

// writePathList renders up to limit paths as a bullet list, with an explicit
// placeholder when the slice is empty (silence confuses small models).
func writePathList(b *strings.Builder, paths []string, limit int, empty string) {
	if len(paths) == 0 {
		b.WriteString(empty)
		b.WriteByte('\n')
		return
	}
	for i, p := range paths {
		if i == limit {
			fmt.Fprintf(b, "- (+%d more)\n", len(paths)-limit)
			return
		}
		fmt.Fprintf(b, "- %s\n", truncate(oneLine(p), maxPathChars))
	}
}

// renderArgs shows a tool call's arguments compactly for the transcript.
func renderArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	enc, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("%v", args)
	}
	return truncate(string(enc), maxArgsChars)
}

// truncate clips s to at most n runes, marking the cut so the model knows the
// content is incomplete rather than finished.
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n]), " \t\n") + "...[truncated]"
}

// oneLine flattens whitespace so a multi-line tool result cannot break the
// line-per-item layout of the prompt.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// pastCalls renders the distinct tool calls already made, most recent last, so
// the model can see which questions are already answered.
func pastCalls(trs []contracts.Exchange) []string {
	seen := make(map[string]bool, len(trs))
	out := make([]string, 0, len(trs))
	for _, ex := range trs {
		sig := ex.Call.Name + "(" + renderArgs(ex.Call.Args) + ")"
		if ex.Call.Name == "" || seen[sig] {
			continue
		}
		seen[sig] = true
		out = append(out, sig)
	}
	// Keep the most recent ones when there are more than fit.
	if len(out) > maxCallsListed {
		out = out[len(out)-maxCallsListed:]
	}
	return out
}
