package tools

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

const (
	// defaultSearchResults is the number of matching lines returned when the
	// model does not ask for a limit.
	defaultSearchResults = 50
	// maxSearchResults caps the model's appetite.
	maxSearchResults = 500
	// maxMatchLineLen caps how much of one matching line is echoed back, so a
	// minified file cannot swallow the context window.
	maxMatchLineLen = 240
)

// searchTool implements contracts.Tool for "search".
type searchTool struct{ sb *sandbox }

func (t *searchTool) Name() string { return contracts.ToolSearch }

func (t *searchTool) Description() string {
	return `search: find a regexp in the repo's source files. args: {"pattern":"func main"}; optional {"path":"subdir","max_results":50}`
}

// Call greps the repo (or a subtree) for a pattern.
//
// The pattern is compiled as a Go regexp; if that fails it is retried as a
// literal string, because small models send unbalanced brackets and stray
// backslashes and an error would waste a whole agent step.
//
// Meta: "matches" []string (unique repo-relative paths, in match order),
// "count" int (number of matching lines reported).
func (t *searchTool) Call(args map[string]any) (contracts.ToolResult, error) {
	pattern, ok := argString(args, patternAliases...)
	if !ok {
		return contracts.ToolResult{}, missingArgErr(contracts.ToolSearch, "pattern", `{"pattern":"func main"}`)
	}
	pattern = cleanPathArg(pattern) // strips stray wrapping quotes/backticks

	re, literal, err := compilePattern(pattern)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: unusable pattern %q: %w", contracts.ToolSearch, pattern, err)
	}

	scope, _ := argString(args, pathAliases...) // optional subtree, default whole repo
	abs, rel, err := t.sb.resolve(scope)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: %w", contracts.ToolSearch, err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return contracts.ToolResult{}, fmt.Errorf("%s: no such path: %s", contracts.ToolSearch, rel)
		}
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot stat %s: %w", contracts.ToolSearch, rel, errNoPath(err))
	}

	// Candidate files: one file, or every source file in the subtree.
	var targets []string
	if info.IsDir() {
		files, _, err := walkTree(abs, t.sb.root)
		if err != nil {
			return contracts.ToolResult{}, fmt.Errorf("%s: cannot walk %s: %w", contracts.ToolSearch, rel, errNoPath(err))
		}
		for _, f := range files {
			targets = append(targets, f.Path)
		}
	} else {
		targets = []string{rel}
	}

	limit := clampInt(argInt(args, defaultSearchResults, "max_results", "maxResults", "limit", "max"),
		defaultSearchResults, 1, maxSearchResults)

	var (
		lines    []string
		matches  []string
		seenPath = make(map[string]struct{})
		hitLimit bool
	)
	for _, target := range targets {
		if len(lines) >= limit {
			hitLimit = true
			break
		}
		data, _, err := readLimited(t.sb.abs(target), maxFileBytes)
		if err != nil || looksBinary(data) {
			continue
		}
		for i, line := range strings.Split(string(data), "\n") {
			if len(lines) >= limit {
				hitLimit = true
				break
			}
			if !re.MatchString(line) {
				continue
			}
			lines = append(lines, fmt.Sprintf("%s:%d: %s", target, i+1, capLine(line)))
			if _, dup := seenPath[target]; !dup {
				seenPath[target] = struct{}{}
				matches = append(matches, target)
			}
		}
	}

	if matches == nil {
		matches = []string{}
	}

	var text string
	switch {
	case len(lines) == 0 && literal:
		text = fmt.Sprintf("no matches for literal %q (the pattern was not a valid regexp)", pattern)
	case len(lines) == 0:
		text = fmt.Sprintf("no matches for pattern %q", pattern)
	default:
		text = strings.Join(lines, "\n")
		if literal {
			text = fmt.Sprintf("(invalid regexp, searched literally for %q)\n", pattern) + text
		}
		if hitLimit {
			text += fmt.Sprintf("\n... [truncated at max_results=%d]", limit)
		}
	}

	return contracts.ToolResult{
		Text: text,
		Meta: map[string]any{
			"matches": matches,
			"count":   len(lines),
		},
	}, nil
}

// compilePattern compiles pattern as a regexp, falling back to a quoted
// literal search when it is not valid. literal reports which happened.
func compilePattern(pattern string) (re *regexp.Regexp, literal bool, err error) {
	if pattern == "" {
		return nil, false, fmt.Errorf("empty pattern")
	}
	if re, err := regexp.Compile(pattern); err == nil {
		return re, false, nil
	}
	re, err = regexp.Compile(regexp.QuoteMeta(pattern))
	if err != nil {
		return nil, false, err // practically unreachable: QuoteMeta always compiles
	}
	return re, true, nil
}

// capLine trims a matching line and shortens it to maxMatchLineLen.
func capLine(line string) string {
	line = strings.TrimSpace(line)
	if len(line) <= maxMatchLineLen {
		return line
	}
	return line[:maxMatchLineLen] + " ..."
}
