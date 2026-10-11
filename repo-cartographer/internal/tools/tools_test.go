package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// ---------------------------------------------------------------------------
// read_file
// ---------------------------------------------------------------------------

func TestReadFile(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go":   "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n",
		"notes.md":  "# notes\n",
		"pkg/a.go":  "package pkg\n",
		"blob.bin":  "head\x00tail",
		"empty.txt": "",
	})
	tool := mustTool(t, root, contracts.ToolReadFile)

	t.Run("reads a file with full meta", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"path": "main.go"})
		if err != nil {
			t.Fatalf("read_file: %v", err)
		}
		if !strings.Contains(res.Text, "func main()") {
			t.Errorf("text = %q, want the file content", res.Text)
		}
		if got := metaString(t, res, "path"); got != "main.go" {
			t.Errorf("meta path = %q, want main.go", got)
		}
		if got := metaString(t, res, "lang"); got != "go" {
			t.Errorf("meta lang = %q, want go", got)
		}
		if got, want := metaInt(t, res, "size"), len("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n"); got != want {
			t.Errorf("meta size = %d, want %d", got, want)
		}
		if metaBool(t, res, "truncated") {
			t.Error("meta truncated = true, want false")
		}
		if imports := metaStrings(t, res, "imports"); !equalStrings(imports, []string{"fmt"}) {
			t.Errorf("meta imports = %v, want [fmt]", imports)
		}
		if strings.Contains(res.Text, root) || strings.Contains(metaString(t, res, "path"), root) {
			t.Error("output leaks the absolute repo root")
		}
	})

	t.Run("truncates at max_bytes", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"path": "main.go", "max_bytes": float64(12)})
		if err != nil {
			t.Fatalf("read_file: %v", err)
		}
		if !strings.HasSuffix(res.Text, truncationMarker) {
			t.Errorf("text = %q, want the %q marker", res.Text, truncationMarker)
		}
		if body := strings.TrimSuffix(res.Text, truncationMarker); body != "package main" {
			t.Errorf("body = %q, want the first 12 bytes", body)
		}
		if !metaBool(t, res, "truncated") {
			t.Error("meta truncated = false, want true")
		}
		if got := metaInt(t, res, "size"); got != 62 {
			t.Errorf("meta size = %d, want the full 62 bytes on disk", got)
		}
	})

	t.Run("defaults and clamps max_bytes", func(t *testing.T) {
		for _, bad := range []any{nil, float64(0), float64(-5), "nonsense", float64(1 << 40)} {
			res, err := tool.Call(map[string]any{"path": "main.go", "max_bytes": bad})
			if err != nil {
				t.Fatalf("max_bytes=%v: %v", bad, err)
			}
			if metaBool(t, res, "truncated") {
				t.Errorf("max_bytes=%v: file should fit in the clamped budget", bad)
			}
		}
	})

	t.Run("empty file", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"path": "empty.txt"})
		if err != nil {
			t.Fatalf("read_file: %v", err)
		}
		if res.Text != "" || metaInt(t, res, "size") != 0 {
			t.Errorf("empty file: text=%q size=%d, want empty", res.Text, metaInt(t, res, "size"))
		}
		if imports := metaStrings(t, res, "imports"); len(imports) != 0 {
			t.Errorf("imports = %v, want empty (non-nil)", imports)
		}
	})

	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			name string
			args map[string]any
		}{
			{"nil args", nil},
			{"no path", map[string]any{}},
			{"empty path value", map[string]any{"path": ""}},
			{"missing file", map[string]any{"path": "nope.go"}},
			{"directory", map[string]any{"path": "pkg"}},
			{"binary file", map[string]any{"path": "blob.bin"}},
			{"escape", map[string]any{"path": "../../etc/passwd"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				res, err := tool.Call(tc.args)
				if err == nil {
					t.Fatalf("want error, got result %q", res.Text)
				}
				if !strings.Contains(err.Error(), contracts.ToolReadFile) {
					t.Errorf("error %q should name the tool", err)
				}
			})
		}
	})

	t.Run("accepts sloppy paths", func(t *testing.T) {
		for _, p := range []any{"./main.go", "/main.go", ` "main.go" `, "`main.go`", "main.go,"} {
			if _, err := tool.Call(map[string]any{"path": p}); err != nil {
				t.Errorf("read_file(%v): unexpected error %v", p, err)
			}
		}
		// Alias keys a model might invent.
		for _, key := range []string{"file", "file_path", "filename", "filepath"} {
			if _, err := tool.Call(map[string]any{key: "main.go"}); err != nil {
				t.Errorf("read_file via %q: unexpected error %v", key, err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// list_dir
// ---------------------------------------------------------------------------

func TestListDir(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go":                   "package main\n",
		"README.md":                 "# hi\n",
		"pkg/a.go":                  "package pkg\n",
		"pkg/sub/b.go":              "package sub\n",
		"zebra.txt":                 "z\n",
		"node_modules/dep/index.js": "x\n",
		".git/config":               "x\n",
		"empty/.keep":               "",
	})
	if err := os.Remove(filepath.Join(root, "empty", ".keep")); err != nil {
		t.Fatal(err)
	}
	tool := mustTool(t, root, contracts.ToolListDir)

	t.Run("root listing marks directories and hides noise", func(t *testing.T) {
		res, err := tool.Call(nil) // nil args must mean the repo root
		if err != nil {
			t.Fatalf("list_dir: %v", err)
		}
		entries := metaStrings(t, res, "entries")
		want := []string{"empty/", "pkg/", "README.md", "main.go", "zebra.txt"}
		if !equalStrings(entries, want) {
			t.Errorf("entries = %v, want %v (dirs first, each group alphabetical)", entries, want)
		}
		if got := metaString(t, res, "path"); got != "." {
			t.Errorf("meta path = %q, want \".\"", got)
		}
		if res.Text != strings.Join(want, "\n") {
			t.Errorf("text = %q, want one entry per line", res.Text)
		}
		for _, e := range entries {
			if strings.Contains(e, "node_modules") || strings.Contains(e, ".git") {
				t.Errorf("entries include skipped %q", e)
			}
		}
	})

	t.Run("subdirectory", func(t *testing.T) {
		for _, p := range []string{"pkg", "pkg/", "./pkg", "/pkg", `"pkg"`} {
			res, err := tool.Call(map[string]any{"path": p})
			if err != nil {
				t.Fatalf("list_dir(%q): %v", p, err)
			}
			if got := metaStrings(t, res, "entries"); !equalStrings(got, []string{"sub/", "a.go"}) {
				t.Errorf("list_dir(%q) entries = %v, want [sub/ a.go]", p, got)
			}
			if got := metaString(t, res, "path"); got != "pkg" {
				t.Errorf("list_dir(%q) meta path = %q, want pkg", p, got)
			}
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"path": "empty"})
		if err != nil {
			t.Fatalf("list_dir: %v", err)
		}
		if got := metaStrings(t, res, "entries"); len(got) != 0 {
			t.Errorf("entries = %v, want empty", got)
		}
		if !strings.Contains(res.Text, "empty") {
			t.Errorf("text = %q, want it to say the directory is empty", res.Text)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args map[string]any
		}{
			{"file not dir", map[string]any{"path": "main.go"}},
			{"missing", map[string]any{"path": "nope"}},
			{"escape", map[string]any{"path": "../.."}},
		} {
			if _, err := tool.Call(tc.args); err == nil {
				t.Errorf("%s: want error, got none", tc.name)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// search
// ---------------------------------------------------------------------------

func TestSearch(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go":        "package main\n\nfunc main() {}\n",
		"pkg/a.go":       "package pkg\n\nfunc Alpha() {}\nfunc Beta() {}\n",
		"pkg/b.go":       "package pkg\n\nfunc Gamma() {}\n",
		"docs/notes.md":  "func main is the entry point\n",
		"node_modules/x": "func main() {}\n",
		"blob.bin":       "func main\x00\n",
	})
	tool := mustTool(t, root, contracts.ToolSearch)

	t.Run("regexp across the repo", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"pattern": `func [A-Z]\w+\(`})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if got := metaStrings(t, res, "matches"); !equalStrings(got, []string{"pkg/a.go", "pkg/b.go"}) {
			t.Errorf("matches = %v, want [pkg/a.go pkg/b.go]", got)
		}
		if got := metaInt(t, res, "count"); got != 3 {
			t.Errorf("count = %d, want 3 matching lines", got)
		}
		for _, line := range strings.Split(res.Text, "\n") {
			parts := strings.SplitN(line, ":", 3)
			if len(parts) != 3 {
				t.Fatalf("line %q is not path:line: text", line)
			}
			if strings.HasPrefix(parts[2], " \t") {
				t.Errorf("line %q should have its text trimmed", line)
			}
		}
		if !strings.Contains(res.Text, "pkg/a.go:3: func Alpha() {}") {
			t.Errorf("text = %q, want a path:line: text entry", res.Text)
		}
	})

	t.Run("skips noise and binaries", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"pattern": "func main"})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, p := range metaStrings(t, res, "matches") {
			if strings.Contains(p, "node_modules") || strings.HasSuffix(p, ".bin") {
				t.Errorf("matches include %q, which should be skipped", p)
			}
		}
		if got := metaStrings(t, res, "matches"); !contains(got, "main.go") || !contains(got, "docs/notes.md") {
			t.Errorf("matches = %v, want main.go and docs/notes.md", got)
		}
	})

	t.Run("invalid regexp falls back to a literal", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"pattern": "func main("})
		if err != nil {
			t.Fatalf("search with invalid regexp should not error: %v", err)
		}
		if got := metaStrings(t, res, "matches"); !equalStrings(got, []string{"main.go"}) {
			t.Errorf("matches = %v, want [main.go] from the literal fallback", got)
		}
		if !strings.Contains(res.Text, "literal") {
			t.Errorf("text = %q, want a note that the pattern was searched literally", res.Text)
		}
	})

	t.Run("subtree scope", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"pattern": "func", "path": "pkg"})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		for _, p := range metaStrings(t, res, "matches") {
			if !strings.HasPrefix(p, "pkg/") {
				t.Errorf("matches include %q, outside the pkg subtree", p)
			}
		}
	})

	t.Run("single file scope", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"pattern": "func", "path": "pkg/a.go"})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if got := metaStrings(t, res, "matches"); !equalStrings(got, []string{"pkg/a.go"}) {
			t.Errorf("matches = %v, want [pkg/a.go]", got)
		}
	})

	t.Run("max_results", func(t *testing.T) {
		for _, limit := range []any{float64(1), 1, "1", json.Number("1")} {
			res, err := tool.Call(map[string]any{"pattern": "func", "max_results": limit})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := metaInt(t, res, "count"); got != 1 {
				t.Errorf("max_results=%v: count = %d, want 1", limit, got)
			}
			if !strings.Contains(res.Text, "truncated") {
				t.Errorf("max_results=%v: text = %q, want a truncation note", limit, res.Text)
			}
		}
	})

	t.Run("no matches", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"pattern": "zzz-not-here"})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if got := metaInt(t, res, "count"); got != 0 {
			t.Errorf("count = %d, want 0", got)
		}
		if got := metaStrings(t, res, "matches"); got == nil || len(got) != 0 {
			t.Errorf("matches = %v, want an empty non-nil slice", got)
		}
		if !strings.Contains(res.Text, "no matches") {
			t.Errorf("text = %q, want a no-matches message", res.Text)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args map[string]any
		}{
			{"nil args", nil},
			{"no pattern", map[string]any{"path": "pkg"}},
			{"empty pattern", map[string]any{"pattern": "  "}},
			{"escape", map[string]any{"pattern": "func", "path": "../.."}},
			{"missing scope", map[string]any{"pattern": "func", "path": "nope"}},
		} {
			if _, err := tool.Call(tc.args); err == nil {
				t.Errorf("%s: want error, got none", tc.name)
			}
		}
	})

	t.Run("pattern aliases and quoted patterns", func(t *testing.T) {
		for _, args := range []map[string]any{
			{"query": "func main"},
			{"regex": "func main"},
			{"pattern": `"func main"`},
		} {
			res, err := tool.Call(args)
			if err != nil {
				t.Fatalf("search(%v): %v", args, err)
			}
			if metaInt(t, res, "count") == 0 {
				t.Errorf("search(%v) found nothing", args)
			}
		}
	})

	t.Run("long lines are capped", func(t *testing.T) {
		long := newRepo(t, map[string]string{"min.js": "var x=1;" + strings.Repeat("y", 1000) + "\n"})
		res, err := mustTool(t, long, contracts.ToolSearch).Call(map[string]any{"pattern": "var x"})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Text) > maxMatchLineLen+100 {
			t.Errorf("text is %d bytes, want the matching line capped", len(res.Text))
		}
	})
}

// ---------------------------------------------------------------------------
// repo_tree
// ---------------------------------------------------------------------------

func TestRepoTree(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go":                   "package main\n",
		"README.md":                 "# hi\n",
		"pkg/a.go":                  "package pkg\n",
		"pkg/b.go":                  "package pkg\n",
		"pkg/deep/c.go":             "package deep\n",
		"cmd/app/main.go":           "package main\n",
		"node_modules/dep/index.js": "x\n",
		".git/HEAD":                 "ref\n",
		"vendor/v/v.go":             "package v\n",
		"blob.bin":                  "a\x00b",
	})
	tool := mustTool(t, root, contracts.ToolRepoTree)

	t.Run("meta files is the complete inventory", func(t *testing.T) {
		res, err := tool.Call(nil)
		if err != nil {
			t.Fatalf("repo_tree: %v", err)
		}
		files, ok := res.Meta["files"].([]contracts.RepoFile)
		if !ok {
			t.Fatalf("meta files is %T, want []contracts.RepoFile", res.Meta["files"])
		}
		want := []string{"README.md", "cmd/app/main.go", "main.go", "pkg/a.go", "pkg/b.go", "pkg/deep/c.go"}
		if got := filePaths(files); !equalStrings(got, want) {
			t.Errorf("meta files = %v, want %v", got, want)
		}
		if got := metaInt(t, res, "count"); got != len(want) {
			t.Errorf("count = %d, want %d", got, len(want))
		}
		for _, f := range files {
			if f.Lang == "" {
				t.Errorf("%s has an empty Lang", f.Path)
			}
			if f.Size <= 0 {
				t.Errorf("%s has size %d", f.Path, f.Size)
			}
			if strings.Contains(f.Path, root) || filepath.IsAbs(f.Path) {
				t.Errorf("%s is not repo-relative", f.Path)
			}
		}
	})

	t.Run("text is an indented tree, dirs first", func(t *testing.T) {
		res, err := tool.Call(map[string]any{})
		if err != nil {
			t.Fatalf("repo_tree: %v", err)
		}
		want := strings.Join([]string{
			".",
			"  cmd/",
			"    app/",
			"      main.go",
			"  pkg/",
			"    deep/",
			"      c.go",
			"    a.go",
			"    b.go",
			"  README.md",
			"  main.go",
		}, "\n")
		if res.Text != want {
			t.Errorf("text =\n%s\nwant\n%s", res.Text, want)
		}
		if strings.Contains(res.Text, "node_modules") || strings.Contains(res.Text, ".git") || strings.Contains(res.Text, "vendor") {
			t.Errorf("tree shows skipped directories:\n%s", res.Text)
		}
	})

	t.Run("max_entries truncates text but not meta", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"max_entries": float64(3)})
		if err != nil {
			t.Fatalf("repo_tree: %v", err)
		}
		if !strings.Contains(res.Text, "truncated") {
			t.Errorf("text = %q, want a truncation note", res.Text)
		}
		if n := strings.Count(res.Text, "\n"); n > 5 {
			t.Errorf("text has %d newlines, want it capped at 3 entries", n)
		}
		files, _ := res.Meta["files"].([]contracts.RepoFile)
		if len(files) != 6 {
			t.Errorf("meta files has %d entries, want all 6 regardless of max_entries", len(files))
		}
	})

	t.Run("max_depth", func(t *testing.T) {
		res, err := tool.Call(map[string]any{"max_depth": 1})
		if err != nil {
			t.Fatalf("repo_tree: %v", err)
		}
		if strings.Contains(res.Text, "a.go") || strings.Contains(res.Text, "app/") {
			t.Errorf("max_depth=1 text should stop at the top level:\n%s", res.Text)
		}
		if !strings.Contains(res.Text, "pkg/") || !strings.Contains(res.Text, "main.go") {
			t.Errorf("max_depth=1 text should still list the top level:\n%s", res.Text)
		}
		files, _ := res.Meta["files"].([]contracts.RepoFile)
		if len(files) != 6 {
			t.Errorf("meta files has %d entries, want all 6 regardless of max_depth", len(files))
		}
	})

	t.Run("empty repo", func(t *testing.T) {
		res, err := mustTool(t, t.TempDir(), contracts.ToolRepoTree).Call(nil)
		if err != nil {
			t.Fatalf("repo_tree: %v", err)
		}
		if !strings.Contains(res.Text, "empty") {
			t.Errorf("text = %q, want an empty-repo message", res.Text)
		}
		if got := metaInt(t, res, "count"); got != 0 {
			t.Errorf("count = %d, want 0", got)
		}
		if files, ok := res.Meta["files"].([]contracts.RepoFile); !ok || len(files) != 0 {
			t.Errorf("meta files = %v, want an empty non-nil slice", res.Meta["files"])
		}
	})

	t.Run("ignores garbage args", func(t *testing.T) {
		for _, args := range []map[string]any{
			nil,
			{"max_depth": "deep"},
			{"max_entries": "all"},
			{"max_depth": -4, "max_entries": float64(-1)},
			{"path": "whatever", "unexpected": []any{1, 2}},
		} {
			if _, err := tool.Call(args); err != nil {
				t.Errorf("repo_tree(%v): unexpected error %v", args, err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// registry
// ---------------------------------------------------------------------------

func TestRegistry(t *testing.T) {
	root := newRepo(t, map[string]string{"main.go": "package main\n"})
	reg, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	wantNames := []string{
		contracts.ToolRepoTree,
		contracts.ToolListDir,
		contracts.ToolReadFile,
		contracts.ToolSearch,
		contracts.ToolFollowImports,
	}

	list := reg.List()
	if len(list) != len(wantNames) {
		t.Fatalf("List has %d tools, want %d", len(list), len(wantNames))
	}
	for i, name := range wantNames {
		if list[i].Name() != name {
			t.Errorf("List()[%d] = %q, want %q (order must be stable)", i, list[i].Name(), name)
		}
	}
	// Mutating the returned slice must not affect the registry.
	list[0] = nil
	if reg.List()[0] == nil {
		t.Error("List returns the registry's own backing array")
	}

	for _, name := range wantNames {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("Get(%q) = not found", name)
		}
		if tool.Name() != name {
			t.Errorf("Get(%q).Name() = %q", name, tool.Name())
		}
		if desc := tool.Description(); !strings.HasPrefix(desc, name+":") {
			t.Errorf("%s description %q should start with the tool name", name, desc)
		}
	}

	if _, ok := reg.Get("no_such_tool"); ok {
		t.Error("Get(\"no_such_tool\") = found, want not found")
	}
	if _, ok := reg.Get(" read_file "); !ok {
		t.Error("Get should tolerate a model's surrounding whitespace")
	}
	if _, ok := reg.Get(`"read_file"`); !ok {
		t.Error("Get should tolerate a model's surrounding quotes")
	}

	infos := reg.Describe()
	if len(infos) != len(wantNames) {
		t.Fatalf("Describe has %d entries, want %d", len(infos), len(wantNames))
	}
	for i, info := range infos {
		if info.Name != wantNames[i] {
			t.Errorf("Describe()[%d].Name = %q, want %q", i, info.Name, wantNames[i])
		}
		if info.Description == "" {
			t.Errorf("Describe()[%d] has no description", i)
		}
		if !strings.Contains(info.Description, "args:") {
			t.Errorf("%s description %q must state its JSON args", info.Name, info.Description)
		}
		if len(info.Description) > 200 {
			t.Errorf("%s description is %d chars; keep it short for a small model", info.Name, len(info.Description))
		}
	}

	if _, err := New(filepath.Join(root, "missing")); err == nil {
		t.Error("New with a missing root: want error")
	}
}

// TestGarbageArgsNeverPanic hammers every tool with the kind of arguments a
// small model actually produces. Tools may error, but must never panic.
func TestGarbageArgsNeverPanic(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go":  "package main\nimport \"fmt\"\n",
		"pkg/a.go": "package pkg\n",
	})
	reg, err := New(root)
	if err != nil {
		t.Fatal(err)
	}

	garbage := []map[string]any{
		nil,
		{},
		{"path": nil},
		{"path": ""},
		{"path": 42},
		{"path": float64(3.5)},
		{"path": true},
		{"path": []any{"main.go"}},
		{"path": map[string]any{"nested": true}},
		{"path": "   "},
		{"path": `"main.go"`},
		{"path": "main.go\n"},
		{"path": strings.Repeat("a/", 500) + "x.go"},
		{"path": "../../../../../../etc/passwd"},
		{"path": "/etc/passwd"},
		{"pattern": nil},
		{"pattern": 7},
		{"pattern": "["},
		{"pattern": `\`},
		{"pattern": "(", "path": "pkg"},
		{"max_bytes": "lots", "path": "main.go"},
		{"max_results": -1, "pattern": "package"},
		{"max_depth": "two", "max_entries": "many"},
		{"unexpected": "key"},
	}

	for _, tool := range reg.List() {
		for _, args := range garbage {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("%s panicked on args %v: %v", tool.Name(), args, r)
					}
				}()
				res, err := tool.Call(args)
				if err != nil {
					if err.Error() == "" {
						t.Errorf("%s returned an empty error for args %v", tool.Name(), args)
					}
					if strings.Contains(err.Error(), root) {
						t.Errorf("%s leaked the repo root in %q", tool.Name(), err)
					}
					return
				}
				if strings.Contains(res.Text, root) {
					t.Errorf("%s leaked the repo root in its text for args %v", tool.Name(), args)
				}
			}()
		}
	}
}
