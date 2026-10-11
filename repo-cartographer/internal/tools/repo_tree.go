package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

const (
	// defaultTreeEntries is how many tree lines are rendered by default.
	defaultTreeEntries = 400
	// maxTreeEntries caps the rendered tree.
	maxTreeEntries = 5000
)

// repoTreeTool implements contracts.Tool for "repo_tree".
type repoTreeTool struct{ sb *sandbox }

func (t *repoTreeTool) Name() string { return contracts.ToolRepoTree }

func (t *repoTreeTool) Description() string {
	return `repo_tree: show the repo's directory tree. args: {} (optional {"max_depth":3,"max_entries":400})`
}

// Call renders the repo tree and returns the full source-file inventory.
//
// Meta: "files" []contracts.RepoFile (EVERY non-skipped source file, never
// truncated by max_entries, which only limits the rendered text), "count" int
// (len(files)).
func (t *repoTreeTool) Call(args map[string]any) (contracts.ToolResult, error) {
	files, dirs, err := walkTree(t.sb.root, t.sb.root)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot walk repo: %w", contracts.ToolRepoTree, errNoPath(err))
	}
	if files == nil {
		files = []contracts.RepoFile{}
	}

	maxDepth := argInt(args, 0, "max_depth", "maxDepth", "depth") // <=0 means unlimited
	if maxDepth < 0 {
		maxDepth = 0
	}
	maxEntries := clampInt(argInt(args, defaultTreeEntries, "max_entries", "maxEntries", "limit"),
		defaultTreeEntries, 1, maxTreeEntries)

	root := buildTree(files, dirs)
	r := &treeRenderer{limit: maxEntries, maxDepth: maxDepth}
	r.render(root, 1)

	var b strings.Builder
	b.WriteString(".\n")
	b.WriteString(strings.Join(r.lines, "\n"))
	if r.truncated {
		fmt.Fprintf(&b, "\n... [truncated: showing %d entries, max_entries=%d]", len(r.lines), maxEntries)
	}
	if len(r.lines) == 0 {
		b.Reset()
		b.WriteString("(empty repo: no source files found)")
	}

	return contracts.ToolResult{
		Text: b.String(),
		Meta: map[string]any{
			"files": files,
			"count": len(files),
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Tree model
// ---------------------------------------------------------------------------

// treeNode is one directory in the rendered tree.
type treeNode struct {
	name  string
	dirs  map[string]*treeNode
	files []string
}

func newTreeNode(name string) *treeNode {
	return &treeNode{name: name, dirs: map[string]*treeNode{}}
}

// child returns (creating if needed) the sub-directory node called name.
func (n *treeNode) child(name string) *treeNode {
	if c, ok := n.dirs[name]; ok {
		return c
	}
	c := newTreeNode(name)
	n.dirs[name] = c
	return c
}

// buildTree assembles a directory tree from the walk results. Directories are
// inserted even when they hold no source files, so an empty package is still
// visible to the reader.
func buildTree(files []contracts.RepoFile, dirs []string) *treeNode {
	root := newTreeNode("")
	for _, d := range dirs {
		cur := root
		for _, part := range strings.Split(d, "/") {
			if part == "" || part == "." {
				continue
			}
			cur = cur.child(part)
		}
	}
	for _, f := range files {
		parts := strings.Split(f.Path, "/")
		cur := root
		for _, part := range parts[:len(parts)-1] {
			if part == "" || part == "." {
				continue
			}
			cur = cur.child(part)
		}
		cur.files = append(cur.files, parts[len(parts)-1])
	}
	return root
}

// treeRenderer flattens a treeNode into indented lines, directories first then
// files, each group alphabetical, stopping at a line and depth budget.
type treeRenderer struct {
	lines     []string
	limit     int
	maxDepth  int // 0 means unlimited
	truncated bool
}

func (r *treeRenderer) render(n *treeNode, depth int) {
	if r.maxDepth > 0 && depth > r.maxDepth {
		if len(n.dirs) > 0 || len(n.files) > 0 {
			r.truncated = true
		}
		return
	}
	names := make([]string, 0, len(n.dirs))
	for name := range n.dirs {
		names = append(names, name)
	}
	sort.Strings(names)
	indent := strings.Repeat("  ", depth) // children of the root sit under "." at one level

	for _, name := range names {
		if !r.add(indent + name + "/") {
			return
		}
		r.render(n.dirs[name], depth+1)
		if r.truncated && len(r.lines) >= r.limit {
			return
		}
	}
	sort.Strings(n.files)
	for _, f := range n.files {
		if !r.add(indent + f) {
			return
		}
	}
}

// add appends one line, reporting false once the budget is exhausted.
func (r *treeRenderer) add(line string) bool {
	if len(r.lines) >= r.limit {
		r.truncated = true
		return false
	}
	r.lines = append(r.lines, line)
	return true
}
