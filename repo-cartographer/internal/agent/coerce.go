package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// Tolerant coercion of ToolResult.Meta values.
//
// A tool in this process hands back the concrete Go types, but the very same
// Meta map may have travelled through JSON (model transport, a cached run, a
// future out-of-process tool server), in which case []string arrives as []any
// of string, int arrives as float64, and a struct arrives as map[string]any.
// Every helper below accepts all of those shapes and returns the zero value
// rather than panicking on anything else - the agent must never die on a tool
// result.

// metaGet reads a key from a possibly-nil Meta map.
func metaGet(meta map[string]any, key string) any {
	if meta == nil {
		return nil
	}
	return meta[key]
}

// asStrings coerces []string, []any-of-string, or a lone string into []string.
func asStrings(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case []string:
		return append([]string(nil), t...)
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := asString(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	default:
		return nil
	}
}

// asString coerces strings (and the stringish scalars JSON may produce).
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

// asInt coerces any numeric shape - notably float64, which is what every
// integer becomes after a JSON round trip.
func asInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int8:
		return int(t)
	case int16:
		return int(t)
	case int32:
		return int(t)
	case int64:
		return int(t)
	case uint:
		return int(t)
	case uint8:
		return int(t)
	case uint16:
		return int(t)
	case uint32:
		return int(t)
	case uint64:
		return int(t)
	case float32:
		return int(t)
	case float64:
		return int(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n)
		}
		if f, err := t.Float64(); err == nil {
			return int(f)
		}
		return 0
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(t)); err == nil {
			return n
		}
		return 0
	default:
		return 0
	}
}

// asBool coerces booleans and their common textual spellings.
func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(t))
		return err == nil && b
	default:
		return false
	}
}

// asRepoFiles coerces repo_tree's Meta["files"] from any of:
//
//	[]contracts.RepoFile          (in-process)
//	[]any of contracts.RepoFile   (loosely built Meta)
//	[]any of map[string]any       (after a JSON round trip)
//	[]any of string               (a bare path list)
func asRepoFiles(v any) []contracts.RepoFile {
	switch t := v.(type) {
	case nil:
		return nil
	case []contracts.RepoFile:
		return append([]contracts.RepoFile(nil), t...)
	case []any:
		out := make([]contracts.RepoFile, 0, len(t))
		for _, e := range t {
			if f, ok := asRepoFile(e); ok {
				out = append(out, f)
			}
		}
		return out
	case []map[string]any:
		out := make([]contracts.RepoFile, 0, len(t))
		for _, m := range t {
			if f, ok := asRepoFile(m); ok {
				out = append(out, f)
			}
		}
		return out
	case []string:
		out := make([]contracts.RepoFile, 0, len(t))
		for _, s := range t {
			if strings.TrimSpace(s) != "" {
				out = append(out, contracts.RepoFile{Path: s})
			}
		}
		return out
	default:
		return nil
	}
}

func asRepoFile(v any) (contracts.RepoFile, bool) {
	switch t := v.(type) {
	case contracts.RepoFile:
		return t, t.Path != ""
	case *contracts.RepoFile:
		if t == nil {
			return contracts.RepoFile{}, false
		}
		return *t, t.Path != ""
	case map[string]any:
		p := asString(t["path"])
		if p == "" {
			return contracts.RepoFile{}, false
		}
		return contracts.RepoFile{
			Path: p,
			Lang: asString(t["lang"]),
			Size: asInt(t["size"]),
		}, true
	case string:
		if strings.TrimSpace(t) == "" {
			return contracts.RepoFile{}, false
		}
		return contracts.RepoFile{Path: t}, true
	default:
		return contracts.RepoFile{}, false
	}
}

// ---------------------------------------------------------------------------
// Decision argument helpers (model-authored args are just as untrusted)
// ---------------------------------------------------------------------------

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	return strings.TrimSpace(asString(args[key]))
}

// withArg returns a copy of args with key set, so vetting never mutates the
// map the Decider handed us.
func withArg(args map[string]any, key string, val any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out[key] = val
	return out
}

// callSignature is a stable, comparable fingerprint of a tool call. json.Marshal
// sorts map keys, so {"path":"a","max_bytes":1} and {"max_bytes":1,"path":"a"}
// produce the same signature and count as the same question.
func callSignature(name string, args map[string]any) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if len(args) == 0 {
		return name + "{}"
	}
	enc, err := json.Marshal(args)
	if err != nil {
		return name + fmt.Sprintf("%v", args)
	}
	return name + string(enc)
}
