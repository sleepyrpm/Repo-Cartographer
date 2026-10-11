package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// ErrNoDecision is returned by ParseDecision when the input contains nothing
// object-shaped at all (pure prose, a refusal, an empty string...).
var ErrNoDecision = errors.New("model: no JSON object found in response")

// ---------------------------------------------------------------------------
// Key aliases
//
// A 1.5B model will happily invent its own schema. Rather than fight it, we
// accept every spelling we have seen in the wild and normalise afterwards.
// ---------------------------------------------------------------------------

var (
	toolKeys = []string{
		"tool", "tool_name", "toolname", "name", "action", "function",
		"command", "cmd", "call", "use", "next_tool", "tool_to_use",
	}
	argsKeys = []string{
		"args", "arguments", "parameters", "params", "input", "inputs",
		"tool_input", "args_json", "argument",
	}
	doneKeys = []string{
		"done", "finished", "finish", "complete", "completed", "stop",
		"is_done", "should_stop", "end", "halt",
	}
	reasonKeys = []string{
		"reason", "thought", "why", "explanation", "rationale", "because",
		"justification", "note", "comment", "message",
	}
	// wrapperKeys hold a nested decision object, e.g.
	// {"tool_call":{"name":"read_file",...}} or {"action":{...}}.
	wrapperKeys = []string{
		"tool_call", "toolcall", "tool_calls", "function_call", "functioncall",
		"action", "decision", "next", "next_action", "step", "response",
		"result", "output", "answer", "choice",
	}
)

// doneAliases are tool names that really mean "I am finished".
var doneAliases = map[string]bool{
	"done": true, "finish": true, "finished": true, "stop": true,
	"end": true, "complete": true, "completed": true, "none": true,
	"null": true, "nil": true, "na": true, "noop": true, "nothing": true,
	"exit": true, "quit": true,
}

// toolAliases maps a compacted (letters+digits only, lowercased) tool name to
// the canonical contracts name. Compaction means read_file, read-file,
// "Read File" and readFile all arrive here as "readfile".
var toolAliases = map[string]string{
	// read_file
	"readfile": contracts.ToolReadFile, "read": contracts.ToolReadFile,
	"openfile": contracts.ToolReadFile, "open": contracts.ToolReadFile,
	"cat": contracts.ToolReadFile, "catfile": contracts.ToolReadFile,
	"viewfile": contracts.ToolReadFile, "view": contracts.ToolReadFile,
	"showfile": contracts.ToolReadFile, "getfile": contracts.ToolReadFile,
	"filecontents": contracts.ToolReadFile, "readsource": contracts.ToolReadFile,
	"readcontents": contracts.ToolReadFile, "fileread": contracts.ToolReadFile,
	"loadfile": contracts.ToolReadFile, "head": contracts.ToolReadFile,

	// list_dir
	"listdir": contracts.ToolListDir, "ls": contracts.ToolListDir,
	"list": contracts.ToolListDir, "dir": contracts.ToolListDir,
	"listdirectory": contracts.ToolListDir, "listfiles": contracts.ToolListDir,
	"lsdir": contracts.ToolListDir, "listfolder": contracts.ToolListDir,
	"readdir": contracts.ToolListDir, "dirlist": contracts.ToolListDir,

	// search
	"search": contracts.ToolSearch, "grep": contracts.ToolSearch,
	"find": contracts.ToolSearch, "rg": contracts.ToolSearch,
	"ripgrep": contracts.ToolSearch, "searchrepo": contracts.ToolSearch,
	"searchcode": contracts.ToolSearch, "grepsearch": contracts.ToolSearch,
	"findtext": contracts.ToolSearch, "findinfiles": contracts.ToolSearch,
	"codesearch": contracts.ToolSearch, "searchfiles": contracts.ToolSearch,

	// follow_imports
	"followimports": contracts.ToolFollowImports, "imports": contracts.ToolFollowImports,
	"import": contracts.ToolFollowImports, "followimport": contracts.ToolFollowImports,
	"deps": contracts.ToolFollowImports, "dependencies": contracts.ToolFollowImports,
	"followdeps": contracts.ToolFollowImports, "getimports": contracts.ToolFollowImports,
	"traceimports": contracts.ToolFollowImports, "importgraph": contracts.ToolFollowImports,

	// repo_tree
	"repotree": contracts.ToolRepoTree, "tree": contracts.ToolRepoTree,
	"filetree": contracts.ToolRepoTree, "treeview": contracts.ToolRepoTree,
	"showtree": contracts.ToolRepoTree, "repostructure": contracts.ToolRepoTree,
	"structure": contracts.ToolRepoTree, "gettree": contracts.ToolRepoTree,
	"repolayout": contracts.ToolRepoTree, "layout": contracts.ToolRepoTree,
	"listrepo": contracts.ToolRepoTree,
}

// smartQuotes rewrites typographic punctuation that models love to emit into
// the ASCII equivalents JSON requires.
var smartQuotes = strings.NewReplacer(
	"“", `"`, // “
	"”", `"`, // ”
	"„", `"`, // „
	"″", `"`, // ″
	"‘", "'", // ‘
	"’", "'", // ’
	"«", `"`, // «
	"»", `"`, // »
	"→", "->", // →
	"\ufeff", "", // byte-order mark
)

// ParseDecision extracts a Decision from realistic small-model output.
//
// It tolerates (at least): surrounding prose and greetings, ```json fences,
// trailing commentary, trailing commas, single-quoted keys/values, Python
// True/False/None, smart quotes, key aliases, done-as-string/number, args as
// an embedded JSON string or a bare path, nested {"tool_call":{...}} wrappers
// and single-element arrays.
//
// Strategy: locate every balanced {...} span with a string-aware brace
// matcher, outermost-and-earliest first, and try each one (raw, then
// repaired) until one yields an object with at least one recognisable key.
//
// An empty tool name with done==false is still a *successful* parse: the
// caller (Client.Decide) treats an unusable tool as a reason to fall back.
func ParseDecision(raw string) (contracts.Decision, error) {
	text := smartQuotes.Replace(raw)
	text = stripFences(text)

	var (
		weak      contracts.Decision // an object that parsed but had no known keys
		haveWeak  bool
		lastErrSn string
	)

	for _, span := range objectCandidates(text) {
		m, ok := unmarshalObject(span)
		if !ok {
			if lastErrSn == "" {
				lastErrSn = truncate(oneLine(span), 80)
			}
			continue
		}
		dec, recognised := decodeDecision(m, 0)
		if recognised {
			return finalise(dec), nil
		}
		if !haveWeak {
			weak, haveWeak = dec, true
		}
	}

	if haveWeak {
		// Something object-shaped came back, it just did not use any key we
		// understand. That is a parse success with an unusable tool.
		return finalise(weak), nil
	}
	if lastErrSn != "" {
		return contracts.Decision{Args: map[string]any{}},
			fmt.Errorf("%w (nearest candidate: %s)", ErrNoDecision, lastErrSn)
	}
	return contracts.Decision{Args: map[string]any{}}, ErrNoDecision
}

// stripFences removes markdown code-fence markers. Brace matching would skip
// them anyway, but removing them keeps the candidate spans clean and stops a
// stray quote inside an info string from confusing the scanner.
func stripFences(s string) string {
	if !strings.Contains(s, "```") {
		return s
	}
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Candidate extraction
// ---------------------------------------------------------------------------

// objectCandidates returns every brace-balanced substring of s, ordered
// outermost-first then earliest-first, followed by a last-resort
// first-'{'..last-'}' span.
//
// The scan is string-aware: braces inside double-quoted strings are ignored
// and \" escapes are respected, so {"text":"} not an object {"} parses
// correctly. Only double quotes open a string: treating a bare apostrophe in
// prose ("don't") as a string delimiter would swallow the real object.
func objectCandidates(s string) []string {
	type span struct{ start, end int } // end is exclusive

	var (
		stack []int
		spans []span
		inStr bool
	)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch c {
			case '\\':
				i++ // skip the escaped byte
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, i)
		case '}':
			if n := len(stack); n > 0 {
				spans = append(spans, span{stack[n-1], i + 1})
				stack = stack[:n-1]
			}
		}
	}

	// Outer objects before their children, earlier objects before later ones.
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})

	out := make([]string, 0, len(spans)+2)
	seen := make(map[string]bool, len(spans)+2)
	add := func(txt string) {
		txt = strings.TrimSpace(txt)
		if txt == "" || seen[txt] {
			return
		}
		seen[txt] = true
		out = append(out, txt)
	}
	for _, sp := range spans {
		add(s[sp.start:sp.end])
	}

	// Truncated generation: an object was opened and never closed. Close it
	// ourselves and let the repairer deal with the rest.
	if len(stack) > 0 {
		add(s[stack[0]:] + strings.Repeat("}", len(stack)))
	}

	// Absolute last resort: the widest possible span, even if unbalanced.
	if first := strings.IndexByte(s, '{'); first >= 0 {
		if last := strings.LastIndexByte(s, '}'); last > first {
			add(s[first : last+1])
		}
	}
	return out
}

// unmarshalObject decodes one candidate, first as-is and then after repair.
func unmarshalObject(text string) (map[string]any, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err == nil && m != nil {
		return m, true
	}
	if err := json.Unmarshal([]byte(repairJSON(text)), &m); err == nil && m != nil {
		return m, true
	}
	return nil, false
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

// maxUnwrapDepth bounds how far decodeDecision descends into wrappers.
const maxUnwrapDepth = 4

// decodeDecision pulls a Decision out of a decoded JSON object, unwrapping
// {"tool_call":{...}}-style envelopes. recognised reports whether any key we
// understand was present; when it is false the caller should prefer another
// candidate.
func decodeDecision(m map[string]any, depth int) (contracts.Decision, bool) {
	dec := contracts.Decision{Args: map[string]any{}}
	if m == nil {
		return dec, false
	}

	// Unwrap an envelope whose single interesting value is the real decision.
	if depth < maxUnwrapDepth {
		if inner, ok := unwrap(m); ok {
			return decodeDecision(inner, depth+1)
		}
	}

	recognised := false

	// --- tool -------------------------------------------------------------
	if _, v, ok := firstKey(m, toolKeys); ok {
		recognised = true
		switch tv := v.(type) {
		case string:
			dec.Tool = tv
		case map[string]any:
			// {"function":{"name":"read_file","arguments":{...}}}
			if _, n, ok := firstKey(tv, toolKeys); ok {
				if s, isStr := n.(string); isStr {
					dec.Tool = s
				}
			}
			if _, av, ok := firstKey(tv, argsKeys); ok {
				dec.Args = coerceArgs(av)
			}
		case []any:
			if len(tv) > 0 {
				if s, isStr := tv[0].(string); isStr {
					dec.Tool = s
				}
			}
		default:
			if v != nil {
				dec.Tool = fmt.Sprint(v)
			}
		}
	}

	// --- args -------------------------------------------------------------
	if _, v, ok := firstKey(m, argsKeys); ok {
		recognised = true
		if a := coerceArgs(v); len(a) > 0 || len(dec.Args) == 0 {
			dec.Args = a
		}
	}

	// --- done -------------------------------------------------------------
	if _, v, ok := firstKey(m, doneKeys); ok {
		recognised = true
		dec.Done = coerceBool(v)
	}

	// --- reason -----------------------------------------------------------
	if _, v, ok := firstKey(m, reasonKeys); ok {
		recognised = true
		if s, isStr := v.(string); isStr {
			dec.Reason = s
		} else if v != nil {
			dec.Reason = fmt.Sprint(v)
		}
	}

	// Flattened form: {"tool":"read_file","path":"main.go"}. Any leftover
	// scalar key that is not an alias is treated as an argument.
	if len(dec.Args) == 0 && dec.Tool != "" {
		for k, v := range m {
			if isAliasKey(k) {
				continue
			}
			switch v.(type) {
			case string, float64, bool, nil:
				dec.Args[strings.ToLower(strings.TrimSpace(k))] = v
			}
		}
	}

	return dec, recognised
}

// unwrap returns the nested decision object of an envelope, if this object is
// one. An envelope is a wrapper key (or a lone key) whose value is an object
// containing a key we recognise.
func unwrap(m map[string]any) (map[string]any, bool) {
	promising := func(v any) (map[string]any, bool) {
		inner, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		for _, set := range [][]string{toolKeys, argsKeys, doneKeys, reasonKeys} {
			if _, _, found := firstKey(inner, set); found {
				return inner, true
			}
		}
		return nil, false
	}

	for _, wk := range wrapperKeys {
		v, ok := lookup(m, wk)
		if !ok {
			continue
		}
		if inner, ok := promising(v); ok {
			return inner, true
		}
		// {"tool_calls":[{...}]}
		if arr, ok := v.([]any); ok && len(arr) > 0 {
			if inner, ok := promising(arr[0]); ok {
				return inner, true
			}
		}
	}

	// A single-key envelope with an unfamiliar name, e.g. {"foo":{...}}.
	if len(m) == 1 {
		for _, v := range m {
			if inner, ok := promising(v); ok {
				return inner, true
			}
			if arr, ok := v.([]any); ok && len(arr) > 0 {
				if inner, ok := promising(arr[0]); ok {
					return inner, true
				}
			}
		}
	}
	return nil, false
}

// lookup finds a key case-insensitively, ignoring spaces, dashes and
// underscores, so "Tool Name" matches "tool_name".
func lookup(m map[string]any, want string) (any, bool) {
	if v, ok := m[want]; ok {
		return v, true
	}
	wantC := compactKey(want)
	for k, v := range m {
		if compactKey(k) == wantC {
			return v, true
		}
	}
	return nil, false
}

// firstKey returns the first of keys present in m (alias precedence order).
func firstKey(m map[string]any, keys []string) (string, any, bool) {
	for _, k := range keys {
		if v, ok := lookup(m, k); ok {
			return k, v, true
		}
	}
	return "", nil, false
}

// isAliasKey reports whether k is one of the schema keys (so it is not an
// argument in the flattened form).
func isAliasKey(k string) bool {
	kc := compactKey(k)
	for _, set := range [][]string{toolKeys, argsKeys, doneKeys, reasonKeys, wrapperKeys} {
		for _, a := range set {
			if compactKey(a) == kc {
				return true
			}
		}
	}
	return false
}

// compactKey lowercases and drops separators for forgiving key comparison.
func compactKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '-', '_', '.', '\t':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// coerceArgs turns whatever landed in the args slot into a non-nil map.
//
//	map            -> used as-is
//	JSON string    -> parsed again ("{\"path\":\"a.go\"}")
//	bare string    -> treated as the "path" argument
//	nil / other    -> empty map
func coerceArgs(v any) map[string]any {
	switch av := v.(type) {
	case map[string]any:
		return av
	case string:
		s := strings.TrimSpace(av)
		if s == "" || s == "{}" || strings.EqualFold(s, "null") || strings.EqualFold(s, "none") {
			return map[string]any{}
		}
		if strings.HasPrefix(s, "{") {
			if m, ok := unmarshalObject(s); ok {
				return m
			}
		}
		// A bare value: overwhelmingly a file path in this tool set.
		return map[string]any{"path": s}
	case []any:
		// ["main.go"] or [{"path":"main.go"}]
		if len(av) == 1 {
			return coerceArgs(av[0])
		}
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

// coerceBool accepts the many shapes "done" arrives in: a real bool, the
// strings "true"/"false"/"yes"/"no"/"1"/"0", or a number.
func coerceBool(v any) bool {
	switch bv := v.(type) {
	case bool:
		return bv
	case string:
		switch strings.ToLower(strings.Trim(strings.TrimSpace(bv), `"'`)) {
		case "true", "yes", "y", "1", "t", "done", "finished", "complete", "ok":
			return true
		default:
			return false
		}
	case float64:
		return bv != 0
	case int:
		return bv != 0
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Normalisation
// ---------------------------------------------------------------------------

// finalise cleans up a decoded decision: canonical tool name, non-nil args,
// tidy reason.
func finalise(dec contracts.Decision) contracts.Decision {
	dec.Tool = NormaliseToolName(dec.Tool)
	if doneAliases[compactKey(dec.Tool)] && dec.Tool != "" {
		// "stop"/"finish" as a tool name means the model wanted to end.
		dec.Tool = ""
		dec.Done = true
	}
	if dec.Args == nil {
		dec.Args = map[string]any{}
	}
	dec.Reason = strings.TrimSpace(oneLine(dec.Reason))
	if len(dec.Reason) > 300 {
		dec.Reason = truncate(dec.Reason, 300)
	}
	return dec
}

// NormaliseToolName cleans a model-supplied tool name and maps near-misses to
// the canonical contracts names (read_file, list_dir, search, follow_imports,
// repo_tree). Unknown names are returned cleaned and lowercased so the caller
// can reject them against the registry.
func NormaliseToolName(name string) string {
	s := strings.TrimSpace(name)
	s = strings.Trim(s, "`\"'*[](){}<>.,;:! \t\n")
	// "functions.read_file" / "tools/read_file" -> "read_file"
	if i := strings.LastIndexAny(s, "./"); i >= 0 && i+1 < len(s) {
		s = s[i+1:]
	}
	// "read_file(path)" -> "read_file"
	if i := strings.IndexAny(s, "("); i > 0 {
		s = s[:i]
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}

	compact := compactKey(s)
	// Strip a redundant "tool" suffix/prefix: "read_file_tool".
	compact = strings.TrimSuffix(compact, "tool")
	compact = strings.TrimPrefix(compact, "tool")

	// Already canonical?
	for _, canon := range []string{
		contracts.ToolReadFile, contracts.ToolListDir, contracts.ToolSearch,
		contracts.ToolFollowImports, contracts.ToolRepoTree,
	} {
		if compact == compactKey(canon) {
			return canon
		}
	}
	if canon, ok := toolAliases[compact]; ok {
		return canon
	}
	if doneAliases[compact] {
		return compact
	}
	// Unknown: hand back a tidy snake_case-ish form for the error message.
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", "_"), "-", "_")
}

// ---------------------------------------------------------------------------
// JSON repair
// ---------------------------------------------------------------------------

// repairJSON rewrites common near-JSON into strict JSON:
//
//   - single-quoted strings become double-quoted
//   - trailing commas before } or ] are dropped
//   - bare words become quoted strings, except true/false/null
//   - Python-ish True/False/None (and yes/no) become true/false/null
//   - raw control characters inside strings are escaped
//   - an unterminated string is closed
//
// It never runs on input that already parsed, so it cannot break valid JSON.
func repairJSON(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 16)

	n := len(s)
	for i := 0; i < n; {
		c := s[i]
		switch {
		case c == '"' || c == '\'':
			i = copyJSONString(&b, s, i)

		case c == ',':
			j := i + 1
			for j < n && isSpaceByte(s[j]) {
				j++
			}
			if j < n && (s[j] == '}' || s[j] == ']') {
				i++ // drop the trailing comma
				continue
			}
			b.WriteByte(c)
			i++

		case c >= '0' && c <= '9', c == '-' && i+1 < n && s[i+1] >= '0' && s[i+1] <= '9':
			j := i
			for j < n && isNumberByte(s[j]) {
				j++
			}
			b.WriteString(s[i:j])
			i = j

		case isIdentStart(c):
			j := i
			for j < n && isIdentByte(s[j]) {
				j++
			}
			b.WriteString(fixBareWord(s[i:j]))
			i = j

		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// copyJSONString copies the string starting at s[i] (quoted with either ' or
// ") into b as a valid double-quoted JSON string, returning the index just
// past it.
func copyJSONString(b *strings.Builder, s string, i int) int {
	delim := s[i]
	n := len(s)
	b.WriteByte('"')
	i++
	for i < n {
		c := s[i]
		if c == '\\' && i+1 < n {
			next := s[i+1]
			if next == '\'' {
				// \' is not a legal JSON escape.
				b.WriteByte('\'')
			} else {
				b.WriteByte('\\')
				b.WriteByte(next)
			}
			i += 2
			continue
		}
		if c == delim {
			b.WriteByte('"')
			return i + 1
		}
		switch {
		case c == '"': // a raw quote inside a single-quoted string
			b.WriteString(`\"`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20:
			fmt.Fprintf(b, `\u%04x`, c)
		default:
			b.WriteByte(c)
		}
		i++
	}
	b.WriteByte('"') // unterminated string: close it
	return n
}

// fixBareWord maps an unquoted word to legal JSON: literals pass through,
// Python/YAML-isms are translated, anything else becomes a quoted string
// (which also fixes unquoted keys such as {tool: "x"}).
func fixBareWord(w string) string {
	switch strings.ToLower(w) {
	case "true", "false", "null":
		return strings.ToLower(w)
	case "none", "nil", "nan", "undefined", "nothing":
		return "null"
	case "yes", "y":
		return "true"
	case "no", "n":
		return "false"
	}
	enc, err := json.Marshal(w)
	if err != nil {
		return `""`
	}
	return string(enc)
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isNumberByte(c byte) bool {
	return (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-'
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '$' || c >= 0x80
}

func isIdentByte(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9') ||
		c == '-' || c == '.' || c == '/' || c == '*' || c == '@' || c == '+' || c == '#'
}
