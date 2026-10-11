package report

import (
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// Input is everything the explore loop learned. The caller (the CLI) builds
// this from the agent's result; it is a plain struct with no behaviour.
type Input struct {
	RootPath       string
	Files          []contracts.RepoFile // discovered inventory, repo-relative slash paths
	Imports        map[string][]string  // resolved edges: file -> files it imports (repo-relative)
	RawImports     map[string][]string  // file -> raw import specs (may include externals)
	Visited        []string             // files the agent actually read, in order
	Notes          []contracts.Finding  // free-form notes the agent gathered (path -> one-liner)
	Searches       []string             // search patterns tried
	StepsUsed      int
	Budget         int
	StoppedBecause string
	ModelName      string // e.g. "qwen2.5:1.5b" (for the provenance line)
	Duration       time.Duration
	MaxNodes       int // diagram node cap; <=0 => DefaultMaxNodes
	MaxStarters    int // <=0 => DefaultMaxStarters
}

// Defaults applied when the corresponding Input field is <= 0.
const (
	DefaultMaxNodes    = 40
	DefaultMaxStarters = 5
)

// maxNodes returns the effective diagram node cap.
func (in Input) maxNodes() int {
	if in.MaxNodes <= 0 {
		return DefaultMaxNodes
	}
	return in.MaxNodes
}

// maxStarters returns the effective starter cap.
func (in Input) maxStarters() int {
	if in.MaxStarters <= 0 {
		return DefaultMaxStarters
	}
	return in.MaxStarters
}

// ---------------------------------------------------------------------------
// Shared, deterministic helpers
// ---------------------------------------------------------------------------

// normPath canonicalises a repo-relative path to forward slashes with no
// leading "./". It returns "" for anything that cannot name a repo file.
func normPath(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" {
		return ""
	}
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "./")
	if p == "" || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return ""
	}
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return ""
	}
	return p
}

// looksLikeFile reports whether a path plausibly names a file inside the repo
// rather than an external package spec that leaked into the resolved edges.
// "internal/tools/read.go" looks like a file; "fmt" and "github.com/x/y" do not.
func looksLikeFile(p string) bool {
	return strings.Contains(path.Base(p), ".")
}

// dirOf returns the grouping directory for a path. Root-level files are
// grouped under "(root)" so they still get a subgraph of their own.
func dirOf(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" || d == "" {
		return rootGroup
	}
	return d
}

const rootGroup = "(root)"

// depthOf counts path segments: "main.go" is 1, "cmd/x/main.go" is 3.
func depthOf(p string) int {
	return strings.Count(p, "/") + 1
}

// uniqueFiles dedupes and normalises the inventory, keeping the first entry
// seen for a path, and returns it sorted by path for determinism.
func uniqueFiles(files []contracts.RepoFile) []contracts.RepoFile {
	seen := make(map[string]bool, len(files))
	out := make([]contracts.RepoFile, 0, len(files))
	for _, f := range files {
		p := normPath(f.Path)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		f.Path = p
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// dedupStrings normalises paths and drops repeats while preserving order.
func dedupStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		p := normPath(s)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// noteIndex maps path -> the first non-empty note the agent recorded for it.
func noteIndex(notes []contracts.Finding) map[string]string {
	out := make(map[string]string, len(notes))
	for _, n := range notes {
		p := normPath(n.Path)
		note := strings.TrimSpace(n.Note)
		if p == "" || note == "" {
			continue
		}
		if _, ok := out[p]; !ok {
			out[p] = note
		}
	}
	return out
}

// nodeSet is the set of paths that may appear in the diagram: everything in
// the inventory, plus any repo file referenced by a resolved import edge even
// if the inventory missed it. External specs are excluded.
func nodeSet(in Input) map[string]bool {
	set := make(map[string]bool, len(in.Files)*2)
	for _, f := range uniqueFiles(in.Files) {
		set[f.Path] = true
	}
	consider := func(raw string) {
		p := normPath(raw)
		if p == "" || set[p] {
			return
		}
		if looksLikeFile(p) {
			set[p] = true
		}
	}
	for from, tos := range in.Imports {
		consider(from)
		for _, to := range tos {
			consider(to)
		}
	}
	return set
}

// sortedNodes returns the node set as a sorted slice.
func sortedNodes(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// edge is a from->to import relationship between two repo files.
type edge struct {
	From string
	To   string
}

// resolvedEdges returns the deduped, sorted import edges that connect two
// known repo files. Self-edges and edges touching unknown targets are dropped.
func resolvedEdges(in Input, set map[string]bool) []edge {
	seen := make(map[edge]bool)
	for from, tos := range in.Imports {
		f := normPath(from)
		if f == "" || !set[f] {
			continue
		}
		for _, to := range tos {
			t := normPath(to)
			if t == "" || t == f || !set[t] {
				continue
			}
			seen[edge{From: f, To: t}] = true
		}
	}
	out := make([]edge, 0, len(seen))
	for e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// degrees returns in-degree and out-degree per path for the given edges.
func degrees(edges []edge) (in, out map[string]int) {
	in, out = map[string]int{}, map[string]int{}
	for _, e := range edges {
		out[e.From]++
		in[e.To]++
	}
	return in, out
}
