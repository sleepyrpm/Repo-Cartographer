package agent

import (
	"fmt"
	"strings"
)

// panicError wraps a value recovered from a panicking tool so the loop can
// treat it as an ordinary tool error.
type panicError struct {
	tool  string
	value any
}

func (e *panicError) Error() string {
	return fmt.Sprintf("tool %s panicked: %v", e.tool, e.value)
}

// clip truncates s to at most n characters, marking that it was cut.
func clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

// oneLine flattens whitespace so a note or a trace line can never break the
// one-line-per-step contract.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func quote(s string) string { return `"` + oneLine(clip(s, 40)) + `"` }

// reasonOf renders a Decision's Reason for the trace.
func reasonOf(reason string) string {
	r := oneLine(reason)
	if r == "" {
		return "(none given)"
	}
	return clip(r, 120)
}

// ---------------------------------------------------------------------------
// Findings
// ---------------------------------------------------------------------------

// declPrefixes are the line openings that announce "this is what the file is".
var declPrefixes = []string{
	"package ", "module ", "class ", "public class ", "final class",
	"def ", "func main(", "int main(", "fn main(", "export default",
	"interface ", "struct ", "namespace ", "program ",
}

// commentMarkers are stripped from a leading doc comment.
var commentMarkers = []string{`"""`, "'''", "/**", "/*", "*/", "//!", "///", "//", "#!", "#", "--", "* ", ";;"}

// summarise produces the one-line Finding note for a file that was just read:
// its declaration line and/or its leading doc comment, plus the import count.
// Never longer than maxFindingNote characters.
func summarise(text string, imports int, truncated bool) string {
	decl, doc := leadLines(text)

	var parts []string
	if decl != "" {
		parts = append(parts, decl)
	}
	if doc != "" && !strings.EqualFold(doc, decl) {
		parts = append(parts, doc)
	}
	note := strings.Join(parts, " - ")
	if note == "" {
		note = "read"
	}
	if imports > 0 {
		note += fmt.Sprintf(" (%d imports)", imports)
	}
	if truncated {
		note += " [truncated]"
	}
	return clip(oneLine(note), maxFindingNote)
}

// leadLines scans the head of a file for a declaration line and a doc comment.
func leadLines(text string) (decl, doc string) {
	var firstCode string
	lines := strings.Split(text, "\n")
	if len(lines) > 60 {
		lines = lines[:60]
	}
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if c, ok := stripComment(line); ok {
			if doc == "" && c != "" {
				doc = c
			}
			continue
		}
		if firstCode == "" {
			firstCode = line
		}
		if decl == "" && hasDeclPrefix(line) {
			decl = line
		}
		if decl != "" && doc != "" {
			break
		}
	}
	if decl == "" && doc == "" {
		decl = firstCode // better than nothing
	}
	return decl, doc
}

func hasDeclPrefix(line string) bool {
	low := strings.ToLower(line)
	for _, p := range declPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// stripComment reports whether line is a comment and returns its text.
func stripComment(line string) (string, bool) {
	for _, m := range commentMarkers {
		if strings.HasPrefix(line, m) {
			return strings.TrimSpace(strings.Trim(strings.TrimPrefix(line, m), `*/"'-`)), true
		}
	}
	return "", false
}
