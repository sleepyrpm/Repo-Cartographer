package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

func TestAsStrings(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"missing key", metaGet(nil, "nope"), nil},
		{"concrete", []string{"a", "b"}, []string{"a", "b"}},
		{"json any", []any{"a", "b"}, []string{"a", "b"}},
		{"json any mixed", []any{"a", 3, nil, "b"}, []string{"a", "b"}},
		{"lone string", "a", []string{"a"}},
		{"blank string", "   ", nil},
		{"wrong type", 42, nil},
		{"map", map[string]any{}, nil},
	}
	for _, c := range cases {
		if got := asStrings(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: asStrings(%#v) = %#v, want %#v", c.name, c.in, got, c.want)
		}
	}
}

func TestAsInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{nil, 0},
		{7, 7},
		{int64(7), 7},
		{int32(7), 7},
		{uint8(7), 7},
		{float64(7), 7},
		{float32(7.9), 7},
		{json.Number("7"), 7},
		{json.Number("7.5"), 7},
		{"7", 7},
		{"nope", 0},
		{true, 0},
		{[]any{1}, 0},
	}
	for _, c := range cases {
		if got := asInt(c.in); got != c.want {
			t.Errorf("asInt(%#v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestAsBool(t *testing.T) {
	for in, want := range map[any]bool{
		nil: false, true: true, false: false, "true": true, "1": true, "no": false, 1: false,
	} {
		if got := asBool(in); got != want {
			t.Errorf("asBool(%#v) = %v, want %v", in, got, want)
		}
	}
}

func TestAsRepoFiles(t *testing.T) {
	want := []contracts.RepoFile{{Path: "a.go", Lang: "go", Size: 3}}
	cases := map[string]any{
		"concrete": []contracts.RepoFile{{Path: "a.go", Lang: "go", Size: 3}},
		"any of struct": []any{
			contracts.RepoFile{Path: "a.go", Lang: "go", Size: 3},
			"", // dropped
		},
		"any of map":     []any{map[string]any{"path": "a.go", "lang": "go", "size": float64(3)}},
		"slice of map":   []map[string]any{{"path": "a.go", "lang": "go", "size": 3}},
		"pointer in any": []any{&contracts.RepoFile{Path: "a.go", Lang: "go", Size: 3}},
	}
	for name, in := range cases {
		if got := asRepoFiles(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: asRepoFiles = %#v, want %#v", name, got, want)
		}
	}
	if got := asRepoFiles([]any{"a.go"}); len(got) != 1 || got[0].Path != "a.go" {
		t.Errorf("bare path list: %#v", got)
	}
	if got := asRepoFiles(nil); got != nil {
		t.Errorf("nil: %#v", got)
	}
	if got := asRepoFiles("a.go"); got != nil {
		t.Errorf("scalar should not coerce: %#v", got)
	}
	if got := asRepoFiles([]any{map[string]any{"lang": "go"}}); len(got) != 0 {
		t.Errorf("entries without a path must be dropped: %#v", got)
	}
}

func TestNormPath(t *testing.T) {
	ok := map[string]string{
		"a.go":               "a.go",
		"./a.go":             "a.go",
		"internal//x/x.go":   "internal/x/x.go",
		"internal/x/../x.go": "", // parent references are rejected outright
		`internal\x\x.go`:    "internal/x/x.go",
		".":                  ".",
		"a/b/c.go":           "a/b/c.go",
		"  spaced/a.go  ":    "spaced/a.go",
	}
	for in, want := range ok {
		got, valid := normPath(in)
		if want == "" {
			if valid {
				t.Errorf("normPath(%q) = %q, want rejected", in, got)
			}
			continue
		}
		if !valid || got != want {
			t.Errorf("normPath(%q) = %q,%v want %q,true", in, got, valid, want)
		}
	}
	for _, bad := range []string{
		"", "   ", "..", "../x.go", "../../etc/passwd", "a/../../b", "/etc/passwd",
		`\\server\share`, "C:/Windows", "c:\\windows",
	} {
		if got, valid := normPath(bad); valid {
			t.Errorf("normPath(%q) = %q, want rejected", bad, got)
		}
	}
}

func TestNormFileAndNormDir(t *testing.T) {
	if _, ok := normFile("."); ok {
		t.Error(`normFile(".") must be rejected: the root is not a file`)
	}
	if _, ok := normFile("internal/"); ok {
		t.Error("normFile must reject a trailing slash")
	}
	if got, ok := normFile("./internal/x.go"); !ok || got != "internal/x.go" {
		t.Errorf("normFile = %q,%v", got, ok)
	}
	if got, ok := normDir(""); !ok || got != "." {
		t.Errorf(`normDir("") = %q,%v want ".",true`, got, ok)
	}
	if got, ok := normDir("internal/x"); !ok || got != "internal/x" {
		t.Errorf("normDir = %q,%v", got, ok)
	}
	if _, ok := normDir("../x"); ok {
		t.Error("normDir must reject a parent reference")
	}
}

func TestUnreadRanking(t *testing.T) {
	a, err := New(Options{Root: "/r", Registry: newRegistry(), Decider: script()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, p := range []string{
		"internal/deep/nested/helper.go",
		"zz.go",
		"go.mod",
		"internal/core.go",
		"cmd/app/main.go",
		"web/index.js",
		"Dockerfile",
		"internal/read.go",
	} {
		a.upsertFile(contracts.RepoFile{Path: p})
	}
	// core.go is imported twice, so it should outrank its tier-mates.
	a.addEdge("internal/read.go", "internal/core.go")
	a.addEdge("zz.go", "internal/core.go")
	a.visited["internal/read.go"] = true
	a.res.Visited = append(a.res.Visited, "internal/read.go")

	got := a.unread(0)
	want := []string{
		"cmd/app/main.go",                // tier 0: cmd/ + main stem
		"web/index.js",                   // tier 0: index stem
		"go.mod",                         // tier 1: manifest (shallower/shorter than Dockerfile)
		"Dockerfile",                     // tier 1: manifest
		"internal/core.go",               // tier 2: most imported
		"zz.go",                          // tier 2: shallowest
		"internal/deep/nested/helper.go", // tier 2: deepest last
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unread order =\n  %v\nwant\n  %v", got, want)
	}
	if a.visited["internal/read.go"] && containsStr(got, "internal/read.go") {
		t.Error("a visited file must not be offered as unread")
	}
	if len(a.unread(2)) != 2 {
		t.Error("unread must honour the limit")
	}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestSummarise(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		imps  int
		trunc bool
		want  []string
	}{
		{
			name: "go doc comment plus package",
			text: "// Package agent runs the loop.\npackage agent\n\nimport \"fmt\"\n",
			imps: 1,
			want: []string{"package agent", "Package agent runs the loop.", "(1 imports)"},
		},
		{
			name: "python class",
			text: "\"\"\"Thing does things.\"\"\"\nimport os\n\nclass Thing:\n    pass\n",
			imps: 1,
			want: []string{"class Thing", "Thing does things."},
		},
		{
			name: "no declaration falls back to first code line",
			text: "\nx = 1\ny = 2\n",
			want: []string{"x = 1"},
		},
		{
			name: "empty file",
			text: "",
			want: []string{"read"},
		},
		{
			name:  "truncated flag",
			text:  "package x\n",
			trunc: true,
			want:  []string{"[truncated]"},
		},
	}
	for _, c := range cases {
		got := summarise(c.text, c.imps, c.trunc)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: summarise = %q, want it to contain %q", c.name, got, w)
			}
		}
		if strings.ContainsAny(got, "\n\t") {
			t.Errorf("%s: summarise must be one line, got %q", c.name, got)
		}
		if len(got) > maxFindingNote {
			t.Errorf("%s: summarise is %d chars, cap is %d", c.name, len(got), maxFindingNote)
		}
	}
	long := summarise("// "+strings.Repeat("verbose ", 100)+"\npackage x\n", 3, false)
	if len(long) > maxFindingNote {
		t.Errorf("long note is %d chars, cap is %d", len(long), maxFindingNote)
	}
}

func TestClipAndOneLine(t *testing.T) {
	if got := clip("abcdef", 4); got != "a..." {
		t.Errorf("clip = %q", got)
	}
	if got := clip("abc", 10); got != "abc" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("abc", 2); got != "ab" {
		t.Errorf("clip = %q", got)
	}
	if got := oneLine(" a\n\tb  c \n"); got != "a b c" {
		t.Errorf("oneLine = %q", got)
	}
}

func TestBriefArgsIsDeterministic(t *testing.T) {
	args := map[string]any{"path": "a.go", "max_bytes": 100, "pattern": "x"}
	want := "{max_bytes:100,path:a.go,pattern:x}"
	for i := 0; i < 20; i++ {
		if got := briefArgs(args); got != want {
			t.Fatalf("briefArgs = %q, want %q", got, want)
		}
	}
	if got := briefArgs(nil); got != "{}" {
		t.Errorf("briefArgs(nil) = %q", got)
	}
}

func TestWithArgDoesNotMutate(t *testing.T) {
	orig := map[string]any{"path": "a.go", "max_bytes": 10}
	got := withArg(orig, "path", "b.go")
	if orig["path"] != "a.go" {
		t.Error("withArg mutated the caller's map")
	}
	if got["path"] != "b.go" || got["max_bytes"] != 10 {
		t.Errorf("withArg = %v", got)
	}
}

func TestDepth(t *testing.T) {
	for in, want := range map[string]int{".": 0, "a.go": 0, "a/b.go": 1, "a/b/c.go": 2} {
		if got := depth(in); got != want {
			t.Errorf("depth(%q) = %d, want %d", in, got, want)
		}
	}
}
