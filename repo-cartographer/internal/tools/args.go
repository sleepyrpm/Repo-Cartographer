package tools

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Argument plumbing. A 1.5B model produces sloppy JSON: missing keys, nil maps,
// numbers where strings belong, floats for integers, paths wrapped in quotes or
// backticks, and plausible-but-wrong key names. Everything here coerces instead
// of failing, and only the tools themselves decide what is genuinely unusable.

// pathAliases are the keys accepted for a path argument. "path" is the frozen
// contract; the rest are common model inventions and cost nothing to honour.
var pathAliases = []string{"path", "file", "file_path", "filepath", "filename", "dir", "directory", "folder"}

// patternAliases are the keys accepted for the search pattern.
var patternAliases = []string{"pattern", "query", "regex", "regexp", "search", "q", "text"}

// argString fetches a string-ish value under the first matching key. Numbers
// and booleans are formatted rather than rejected. The returned bool reports
// whether any of the keys was present with a non-empty value.
func argString(args map[string]any, keys ...string) (string, bool) {
	if args == nil {
		return "", false
	}
	for _, k := range keys {
		v, ok := args[k]
		if !ok || v == nil {
			continue
		}
		s, ok := stringify(v)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		return s, true
	}
	return "", false
}

// stringify renders a JSON-decoded value as a string.
func stringify(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case json.Number:
		return t.String(), true
	case float64:
		return formatFloat(t), true
	case float32:
		return formatFloat(float64(t)), true
	case int:
		return strconv.Itoa(t), true
	case int32:
		return strconv.FormatInt(int64(t), 10), true
	case int64:
		return strconv.FormatInt(t, 10), true
	case bool:
		return strconv.FormatBool(t), true
	case []any:
		// Models sometimes send {"path":["a","b"]}; take the first usable item.
		for _, item := range t {
			if s, ok := stringify(item); ok && strings.TrimSpace(s) != "" {
				return s, true
			}
		}
		return "", false
	default:
		return "", false
	}
}

// formatFloat prints a JSON number without a pointless ".0" tail.
func formatFloat(f float64) string {
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// argInt fetches an integer-ish value under the first matching key, falling
// back to def when absent or unusable. JSON numbers arrive as float64; ints,
// int64, json.Number and numeric strings are all accepted.
func argInt(args map[string]any, def int, keys ...string) int {
	if args == nil {
		return def
	}
	for _, k := range keys {
		v, ok := args[k]
		if !ok || v == nil {
			continue
		}
		if n, ok := toInt(v); ok {
			return n
		}
	}
	return def
}

// toInt coerces a single JSON-decoded value to an int.
func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return int(t), true
	case float32:
		return int(t), true
	case int:
		return t, true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
		if f, err := t.Float64(); err == nil {
			return int(f), true
		}
		return 0, false
	case string:
		s := strings.TrimSpace(strings.Trim(strings.TrimSpace(t), "\"'`"))
		if s == "" {
			return 0, false
		}
		if n, err := strconv.Atoi(s); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return int(f), true
		}
		return 0, false
	case []any:
		for _, item := range t {
			if n, ok := toInt(item); ok {
				return n, true
			}
		}
		return 0, false
	default:
		return 0, false
	}
}

// cleanPathArg normalises a model-supplied path: surrounding whitespace,
// quotes and backticks are stripped (repeatedly, for `"x"` style doubles), as
// are trailing commas and semicolons left over from copied JSON.
func cleanPathArg(s string) string {
	s = strings.TrimSpace(s)
	for len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	s = strings.TrimSpace(strings.Trim(s, ",;"))
	return strings.TrimSpace(s)
}

// clampInt keeps a model-supplied limit inside sane bounds, substituting def
// for values that are zero or negative.
func clampInt(v, def, min, max int) int {
	if v <= 0 {
		v = def
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return v
}

// missingArgErr is the error returned when a required argument is absent.
func missingArgErr(tool, key, example string) error {
	return fmt.Errorf("%s: missing %q argument, expected args like %s", tool, key, example)
}
