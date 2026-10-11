package tools

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// newRepo builds a throwaway fixture repository from a map of repo-relative
// slash paths to contents and returns its root. Intermediate directories are
// created automatically; an entry whose content is "" still creates the file.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// mustTool fetches one tool from a fresh registry rooted at root.
func mustTool(t *testing.T, root, name string) contracts.Tool {
	t.Helper()
	reg, err := New(root)
	if err != nil {
		t.Fatalf("New(%q): %v", root, err)
	}
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("registry has no tool %q", name)
	}
	return tool
}

// metaStrings reads a []string meta value, failing the test if it is absent or
// of the wrong type.
func metaStrings(t *testing.T, res contracts.ToolResult, key string) []string {
	t.Helper()
	v, ok := res.Meta[key]
	if !ok {
		t.Fatalf("meta is missing key %q (meta=%v)", key, res.Meta)
	}
	ss, ok := v.([]string)
	if !ok {
		t.Fatalf("meta[%q] is %T, want []string", key, v)
	}
	return ss
}

// metaInt reads an int meta value.
func metaInt(t *testing.T, res contracts.ToolResult, key string) int {
	t.Helper()
	v, ok := res.Meta[key]
	if !ok {
		t.Fatalf("meta is missing key %q (meta=%v)", key, res.Meta)
	}
	n, ok := v.(int)
	if !ok {
		t.Fatalf("meta[%q] is %T, want int", key, v)
	}
	return n
}

// metaString reads a string meta value.
func metaString(t *testing.T, res contracts.ToolResult, key string) string {
	t.Helper()
	v, ok := res.Meta[key]
	if !ok {
		t.Fatalf("meta is missing key %q (meta=%v)", key, res.Meta)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("meta[%q] is %T, want string", key, v)
	}
	return s
}

// metaBool reads a bool meta value.
func metaBool(t *testing.T, res contracts.ToolResult, key string) bool {
	t.Helper()
	v, ok := res.Meta[key]
	if !ok {
		t.Fatalf("meta is missing key %q (meta=%v)", key, res.Meta)
	}
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("meta[%q] is %T, want bool", key, v)
	}
	return b
}

// filePaths extracts the sorted paths of a []contracts.RepoFile.
func filePaths(files []contracts.RepoFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

// equalStrings compares two string slices element-wise.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// contains reports whether want is in got.
func contains(got []string, want string) bool {
	for _, g := range got {
		if g == want {
			return true
		}
	}
	return false
}

// mustExist reports whether a repo-relative path names a regular file in root.
func mustExist(t *testing.T, root, rel string) bool {
	t.Helper()
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil && info.Mode().IsRegular()
}
