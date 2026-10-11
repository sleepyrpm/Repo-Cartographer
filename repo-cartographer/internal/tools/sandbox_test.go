package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewSandboxValidatesRoot(t *testing.T) {
	root := newRepo(t, map[string]string{"a.go": "package a\n"})

	if _, err := newSandbox(""); err == nil {
		t.Error("empty root: want error")
	}
	if _, err := newSandbox(filepath.Join(root, "nope")); err == nil {
		t.Error("missing root: want error")
	}
	if _, err := newSandbox(filepath.Join(root, "a.go")); err == nil {
		t.Error("file as root: want error")
	}
	if _, err := newSandbox(root); err != nil {
		t.Errorf("valid root: unexpected error %v", err)
	}
	if _, err := New(filepath.Join(root, "a.go")); err == nil {
		t.Error("New with file root: want error")
	}
}

func TestSandboxResolveAccepts(t *testing.T) {
	root := newRepo(t, map[string]string{
		"a.go":       "package a\n",
		"pkg/b.go":   "package pkg\n",
		"pkg/sub/c":  "c\n",
		"spaces .go": "x\n",
	})
	sb, err := newSandbox(root)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		in      string
		wantRel string
	}{
		{"empty means root", "", "."},
		{"dot", ".", "."},
		{"dot slash", "./", "."},
		{"plain file", "a.go", "a.go"},
		{"dot-slash file", "./a.go", "a.go"},
		{"nested", "pkg/b.go", "pkg/b.go"},
		{"leading slash is root relative", "/pkg/b.go", "pkg/b.go"},
		{"internal dot dot stays inside", "pkg/sub/../b.go", "pkg/b.go"},
		{"host absolute inside root", filepath.Join(root, "pkg", "b.go"), "pkg/b.go"},
		{"quoted", `"pkg/b.go"`, "pkg/b.go"},
		{"single quoted", `'pkg/b.go'`, "pkg/b.go"},
		{"backticked", "`pkg/b.go`", "pkg/b.go"},
		{"whitespace padded", "  pkg/b.go \n", "pkg/b.go"},
		{"trailing comma", "pkg/b.go,", "pkg/b.go"},
		{"trailing slash dir", "pkg/", "pkg"},
		{"name with space", "spaces .go", "spaces .go"},
		{"nonexistent but inside", "does/not/exist.go", "does/not/exist.go"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			abs, rel, err := sb.resolve(tc.in)
			if err != nil {
				t.Fatalf("resolve(%q): unexpected error %v", tc.in, err)
			}
			if rel != tc.wantRel {
				t.Errorf("resolve(%q) rel = %q, want %q", tc.in, rel, tc.wantRel)
			}
			if !strings.HasPrefix(abs, root) {
				t.Errorf("resolve(%q) abs = %q, want inside %q", tc.in, abs, root)
			}
			if strings.Contains(rel, "\\") {
				t.Errorf("resolve(%q) rel = %q, want slash separated", tc.in, rel)
			}
		})
	}
}

func TestSandboxResolveRejectsEscapes(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := newRepo(t, map[string]string{"a.go": "package a\n", "pkg/b.go": "package pkg\n"})

	// A symlink inside the repo pointing at the outside directory, and one
	// pointing at a file outside it.
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt")); err != nil {
		t.Fatal(err)
	}
	// A symlink that stays inside is fine and must keep working.
	if err := os.Symlink(filepath.Join(root, "pkg"), filepath.Join(root, "inside")); err != nil {
		t.Fatal(err)
	}

	sb, err := newSandbox(root)
	if err != nil {
		t.Fatal(err)
	}

	bad := []struct {
		name string
		in   string
	}{
		{"parent", ".."},
		{"parent file", "../secret.txt"},
		{"deep parent", "../../etc/passwd"},
		{"climb back out", "pkg/../../secret.txt"},
		{"absolute outside", "/etc/passwd"},
		{"absolute outside real", filepath.Join(outside, "secret.txt")},
		{"symlinked dir", "escape"},
		{"through symlinked dir", "escape/secret.txt"},
		{"symlinked file", "leak.txt"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := sb.resolve(tc.in)
			if err == nil {
				t.Fatalf("resolve(%q): want error, got none", tc.in)
			}
			if !strings.Contains(err.Error(), "escapes repo root") {
				t.Fatalf("resolve(%q): error = %v, want it to mention escaping the repo root", tc.in, err)
			}
			if strings.Contains(err.Error(), root) {
				t.Errorf("resolve(%q): error %q leaks the absolute repo root", tc.in, err)
			}
		})
	}

	if _, rel, err := sb.resolve("inside/b.go"); err != nil || rel != "inside/b.go" {
		t.Errorf("resolve through an inside symlink = (%q, %v), want (\"inside/b.go\", nil)", rel, err)
	}

	// The read_file tool must refuse the same paths.
	rf := mustTool(t, root, "read_file")
	for _, tc := range bad {
		if _, err := rf.Call(map[string]any{"path": tc.in}); err == nil {
			t.Errorf("read_file(%q): want error, got none", tc.in)
		}
	}
}

func TestDetectLang(t *testing.T) {
	tests := []struct{ path, want string }{
		{"main.go", "go"},
		{"a/b/c.go", "go"},
		{"script.py", "python"},
		{"app.js", "javascript"},
		{"app.mjs", "javascript"},
		{"ui.jsx", "jsx"},
		{"types.ts", "typescript"},
		{"ui.tsx", "tsx"},
		{"Main.java", "java"},
		{"lib.c", "c"},
		{"lib.cpp", "cpp"},
		{"lib.cc", "cpp"},
		{"lib.h", "h"},
		{"lib.hpp", "hpp"},
		{"main.rs", "rust"},
		{"app.rb", "ruby"},
		{"index.php", "php"},
		{"run.sh", "shell"},
		{"README.md", "markdown"},
		{"data.json", "json"},
		{"conf.yaml", "yaml"},
		{"conf.yml", "yaml"},
		{"Cargo.toml", "toml"},
		{"page.html", "html"},
		{"style.css", "css"},
		{"q.sql", "sql"},
		{"Dockerfile", "dockerfile"},
		{"Dockerfile.dev", "dockerfile"},
		{"Makefile", "makefile"},
		{"GNUmakefile", "makefile"},
		{"go.mod", "gomod"},
		{"go.sum", "gomod"},
		{"package.json", "json"},
		{"/abs/path/main.go", "go"},
		{"notes.txt", "text"},
		{"LICENSE", "text"},
		{"weird.xyz", "text"},
		{"", "text"},
	}
	for _, tc := range tests {
		if got := DetectLang(tc.path); got != tc.want {
			t.Errorf("DetectLang(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestSourceFilesSkipsNoise(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go":                    "package main\n",
		"pkg/util.go":                "package pkg\n",
		"README.md":                  "# hi\n",
		".git/config":                "[core]\n",
		"node_modules/dep/index.js":  "module.exports = 1\n",
		"vendor/x/y.go":              "package x\n",
		"dist/bundle.js":             "x\n",
		"build/out.o":                "x\n",
		"target/debug/app":           "x\n",
		".venv/lib/py.py":            "x\n",
		"venv/lib/py.py":             "x\n",
		"__pycache__/mod.pyc":        "x\n",
		".idea/workspace.xml":        "<x/>\n",
		".vscode/settings.json":      "{}\n",
		".next/build.js":             "x\n",
		".cache/thing":               "x\n",
		"nested/node_modules/a/b.js": "x\n",
		"assets/blob.bin":            "ok\x00binary\n",
		"deep/a/b/c/d.py":            "x = 1\n",
	})
	// A file over the size cap must be skipped too.
	big := strings.Repeat("x", maxFileBytes+10)
	if err := os.WriteFile(filepath.Join(root, "huge.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := SourceFiles(root)
	if err != nil {
		t.Fatalf("SourceFiles: %v", err)
	}
	got := filePaths(files)
	want := []string{"README.md", "deep/a/b/c/d.py", "main.go", "pkg/util.go"}
	if !equalStrings(got, want) {
		t.Errorf("SourceFiles = %v, want %v", got, want)
	}
	for _, f := range files {
		if f.Path == "main.go" {
			if f.Lang != "go" {
				t.Errorf("main.go lang = %q, want go", f.Lang)
			}
			if f.Size != len("package main\n") {
				t.Errorf("main.go size = %d, want %d", f.Size, len("package main\n"))
			}
		}
		if filepath.IsAbs(f.Path) {
			t.Errorf("SourceFiles returned an absolute path %q", f.Path)
		}
	}

	if _, err := SourceFiles(filepath.Join(root, "missing")); err == nil {
		t.Error("SourceFiles on a missing root: want error")
	}
}

func TestSourceFilesSkipsSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := newRepo(t, map[string]string{"main.go": "package main\n"})
	if err := os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(root, "linked.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	files, err := SourceFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := filePaths(files); !equalStrings(got, []string{"main.go"}) {
		t.Errorf("SourceFiles = %v, want [main.go] (symlinks must be ignored)", got)
	}
}

func TestLooksBinary(t *testing.T) {
	if !looksBinary([]byte("abc\x00def")) {
		t.Error("buffer with NUL should look binary")
	}
	if looksBinary([]byte("plain text\n")) {
		t.Error("plain text should not look binary")
	}
	// A NUL beyond the sniff window is not inspected.
	late := append([]byte(strings.Repeat("a", binarySniffBytes)), 0)
	if looksBinary(late) {
		t.Error("NUL past the sniff window should be ignored")
	}
}

func TestArgCoercion(t *testing.T) {
	t.Run("strings", func(t *testing.T) {
		tests := []struct {
			name string
			args map[string]any
			want string
			ok   bool
		}{
			{"nil map", nil, "", false},
			{"missing", map[string]any{"other": "x"}, "", false},
			{"nil value", map[string]any{"path": nil}, "", false},
			{"empty", map[string]any{"path": "   "}, "", false},
			{"plain", map[string]any{"path": "a.go"}, "a.go", true},
			{"alias file", map[string]any{"file": "a.go"}, "a.go", true},
			{"alias file_path", map[string]any{"file_path": "a.go"}, "a.go", true},
			{"number", map[string]any{"path": float64(12)}, "12", true},
			{"json number", map[string]any{"path": json.Number("7")}, "7", true},
			{"int", map[string]any{"path": 3}, "3", true},
			{"bool", map[string]any{"path": true}, "true", true},
			{"list", map[string]any{"path": []any{"a.go", "b.go"}}, "a.go", true},
			{"wrong type", map[string]any{"path": map[string]any{"a": 1}}, "", false},
		}
		for _, tc := range tests {
			got, ok := argString(tc.args, pathAliases...)
			if got != tc.want || ok != tc.ok {
				t.Errorf("%s: argString = (%q,%v), want (%q,%v)", tc.name, got, ok, tc.want, tc.ok)
			}
		}
	})

	t.Run("ints", func(t *testing.T) {
		tests := []struct {
			name string
			args map[string]any
			want int
		}{
			{"nil map", nil, 42},
			{"missing", map[string]any{}, 42},
			{"float64", map[string]any{"max_bytes": float64(10)}, 10},
			{"float with fraction", map[string]any{"max_bytes": 10.7}, 10},
			{"int", map[string]any{"max_bytes": 11}, 11},
			{"int64", map[string]any{"max_bytes": int64(12)}, 12},
			{"json number", map[string]any{"max_bytes": json.Number("13")}, 13},
			{"numeric string", map[string]any{"max_bytes": "14"}, 14},
			{"quoted numeric string", map[string]any{"max_bytes": `"15"`}, 15},
			{"garbage string", map[string]any{"max_bytes": "lots"}, 42},
			{"nil value", map[string]any{"max_bytes": nil}, 42},
			{"camel alias", map[string]any{"maxBytes": float64(16)}, 16},
		}
		for _, tc := range tests {
			if got := argInt(tc.args, 42, "max_bytes", "maxBytes"); got != tc.want {
				t.Errorf("%s: argInt = %d, want %d", tc.name, got, tc.want)
			}
		}
	})

	t.Run("clean path arg", func(t *testing.T) {
		tests := []struct{ in, want string }{
			{"  a.go ", "a.go"},
			{`"a.go"`, "a.go"},
			{`'a.go'`, "a.go"},
			{"`a.go`", "a.go"},
			{`""a.go""`, "a.go"},
			{"a.go;", "a.go"},
			{"", ""},
			{`"`, `"`},
		}
		for _, tc := range tests {
			if got := cleanPathArg(tc.in); got != tc.want {
				t.Errorf("cleanPathArg(%q) = %q, want %q", tc.in, got, tc.want)
			}
		}
	})

	t.Run("clamp", func(t *testing.T) {
		if got := clampInt(0, 50, 1, 500); got != 50 {
			t.Errorf("clampInt(0) = %d, want the default 50", got)
		}
		if got := clampInt(-3, 50, 1, 500); got != 50 {
			t.Errorf("clampInt(-3) = %d, want the default 50", got)
		}
		if got := clampInt(9000, 50, 1, 500); got != 500 {
			t.Errorf("clampInt(9000) = %d, want the max 500", got)
		}
		if got := clampInt(7, 50, 1, 500); got != 7 {
			t.Errorf("clampInt(7) = %d, want 7", got)
		}
	})
}
