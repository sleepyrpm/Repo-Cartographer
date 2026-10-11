package agent

import (
	"path"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// fold is step 4 of the cycle: turn a ToolResult into knowledge.
//
// Each tool contributes a different kind of fact, and every Meta value is read
// through the tolerant coercion helpers in coerce.go because Meta may be nil,
// may be missing keys, and may hold JSON-round-tripped shapes ([]any, float64)
// instead of the concrete Go types.
//
// It returns the canonical path of the file that was read, and true, when the
// call was a successful read_file - that is the trigger for auto-following
// imports.
func (a *Agent) fold(dec contracts.Decision, res contracts.ToolResult) (string, bool) {
	switch dec.Tool {
	case contracts.ToolRepoTree:
		a.foldRepoTree(res)
	case contracts.ToolReadFile:
		return a.foldReadFile(dec, res)
	case contracts.ToolFollowImports:
		a.foldFollowImports(argString(dec.Args, "path"), res)
	case contracts.ToolSearch:
		a.foldSearch(dec, res)
	case contracts.ToolListDir:
		a.foldListDir(dec, res)
	}
	return "", false
}

// foldRepoTree seeds (or refreshes) the file inventory and the tree snapshot.
// Meta keys consumed: "files" ([]contracts.RepoFile), "count" (int, trace only).
func (a *Agent) foldRepoTree(res contracts.ToolResult) {
	if strings.TrimSpace(res.Text) != "" {
		a.tree = res.Text
	}
	for _, f := range asRepoFiles(metaGet(res.Meta, "files")) {
		a.upsertFile(f)
	}
}

// foldReadFile is where most of the map gets drawn.
// Meta keys consumed: "path" (string), "lang" (string), "size" (int),
// "truncated" (bool), "imports" ([]string).
func (a *Agent) foldReadFile(dec contracts.Decision, res contracts.ToolResult) (string, bool) {
	// Prefer the tool's own idea of the path; fall back to what we asked for.
	p, ok := normFile(asString(metaGet(res.Meta, "path")))
	if !ok {
		p, ok = normFile(argString(dec.Args, "path"))
	}
	if !ok {
		return "", false // nothing trustworthy to attribute this read to
	}

	// Visited, in read order, deduped.
	if !a.visited[p] {
		a.visited[p] = true
		a.res.Visited = append(a.res.Visited, p)
	}

	// Grow the inventory with what the read taught us.
	size := asInt(metaGet(res.Meta, "size"))
	if size == 0 {
		size = len(res.Text)
	}
	a.upsertFile(contracts.RepoFile{
		Path: p,
		Lang: asString(metaGet(res.Meta, "lang")),
		Size: size,
	})

	// Raw import specs, exactly as written in the source.
	raw := asStrings(metaGet(res.Meta, "imports"))
	a.mergeRaw(p, raw)

	// One short, human-useful note per file.
	note := summarise(res.Text, len(raw), asBool(metaGet(res.Meta, "truncated")))
	a.res.Findings = append(a.res.Findings, contracts.Finding{Path: p, Note: note})

	return p, true
}

// foldFollowImports records dependency edges.
// Meta keys consumed: "path" (string, optional), "imports" ([]string),
// "resolved" ([]string - repo-relative paths that exist in the repo).
func (a *Agent) foldFollowImports(fallbackPath string, res contracts.ToolResult) {
	p, ok := normFile(asString(metaGet(res.Meta, "path")))
	if !ok {
		p, ok = normFile(fallbackPath)
	}
	if !ok {
		return
	}
	a.mergeRaw(p, asStrings(metaGet(res.Meta, "imports")))
	for _, dep := range asStrings(metaGet(res.Meta, "resolved")) {
		d, ok := normFile(dep)
		if !ok || d == p {
			continue
		}
		a.addEdge(p, d)
		a.upsertFile(contracts.RepoFile{Path: d})
	}
}

// foldSearch records the pattern and any newly discovered files.
// Meta keys consumed: "matches" ([]string), "count" (int, trace only).
func (a *Agent) foldSearch(dec contracts.Decision, res contracts.ToolResult) {
	if pat := argString(dec.Args, "pattern"); pat != "" && !a.searched[pat] {
		a.searched[pat] = true
		a.res.Searches = append(a.res.Searches, pat)
	}
	for _, m := range asStrings(metaGet(res.Meta, "matches")) {
		if p, ok := normFile(m); ok {
			a.upsertFile(contracts.RepoFile{Path: p})
		}
	}
}

// foldListDir records newly seen files in a directory.
// Meta keys consumed: "entries" ([]string, directories end in "/"),
// "path" (string, the listed directory).
func (a *Agent) foldListDir(dec contracts.Decision, res contracts.ToolResult) {
	dir := asString(metaGet(res.Meta, "path"))
	if dir == "" {
		dir = argString(dec.Args, "path")
	}
	dir, ok := normDir(dir)
	if !ok {
		return
	}
	for _, e := range asStrings(metaGet(res.Meta, "entries")) {
		e = strings.TrimSpace(e)
		if e == "" || strings.HasSuffix(e, "/") {
			continue // a directory, not a file
		}
		joined := e
		if dir != "." {
			joined = path.Join(dir, e)
		}
		if p, ok := normFile(joined); ok {
			a.upsertFile(contracts.RepoFile{Path: p})
		}
	}
}

// ---------------------------------------------------------------------------
// small state mutators
// ---------------------------------------------------------------------------

// upsertFile adds a file to the inventory, or enriches the entry already there
// (a path discovered by search has no Lang/Size until someone reads it).
func (a *Agent) upsertFile(f contracts.RepoFile) {
	p, ok := normFile(f.Path)
	if !ok {
		return
	}
	f.Path = p
	if i, seen := a.fileAt[p]; seen {
		if f.Lang != "" {
			a.res.Files[i].Lang = f.Lang
		}
		if f.Size > 0 {
			a.res.Files[i].Size = f.Size
		}
		return
	}
	a.fileAt[p] = len(a.res.Files)
	a.res.Files = append(a.res.Files, f)
}

func (a *Agent) addEdge(from, to string) {
	set := a.edges[from]
	if set == nil {
		set = map[string]bool{}
		a.edges[from] = set
	}
	if set[to] {
		return
	}
	set[to] = true
	a.res.Imports[from] = append(a.res.Imports[from], to)
}

func (a *Agent) mergeRaw(p string, specs []string) {
	if len(specs) == 0 {
		if _, ok := a.res.RawImports[p]; !ok {
			a.res.RawImports[p] = nil
		}
		return
	}
	have := map[string]bool{}
	for _, s := range a.res.RawImports[p] {
		have[s] = true
	}
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" || have[s] {
			continue
		}
		have[s] = true
		a.res.RawImports[p] = append(a.res.RawImports[p], s)
	}
}
