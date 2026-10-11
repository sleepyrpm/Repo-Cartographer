package agent

import (
	"path"
	"sort"
	"strings"
)

// unread returns up to limit source files the agent has not read yet, best
// candidate first.
//
// The ranking matters more than it looks: a 1.5B model will usually just pick
// the first item it is shown, so this heuristic is effectively the agent's
// taste. The order is
//
//	tier 0  likely entrypoints        main.*, cmd/**, index.*, app.*, server.*, __main__.py
//	tier 1  config / manifest files   go.mod, package.json, Makefile, Dockerfile, *.toml, *.yaml, ...
//	tier 2  everything else
//
// and inside a tier: most-imported first (by the edges discovered so far),
// then shallowest, then shortest, then alphabetical - so the list is fully
// deterministic.
func (a *Agent) unread(limit int) []string {
	// in-degree from the edges we have resolved so far
	inDeg := map[string]int{}
	for _, tos := range a.res.Imports {
		for _, to := range tos {
			inDeg[to]++
		}
	}

	cands := make([]string, 0, len(a.res.Files))
	for _, f := range a.res.Files {
		if f.Path == "" || a.visited[f.Path] {
			continue
		}
		cands = append(cands, f.Path)
	}

	sort.SliceStable(cands, func(i, j int) bool {
		pi, pj := cands[i], cands[j]
		ti, tj := tier(pi), tier(pj)
		if ti != tj {
			return ti < tj
		}
		// Inside the entrypoint tier, a real main() beats a conventional
		// directory: cmd/app/main.go before web/index.js before cmd/other.go.
		if ti == 0 {
			if si, sj := entryScore(pi), entryScore(pj); si != sj {
				return si < sj
			}
		}
		if di, dj := inDeg[pi], inDeg[pj]; di != dj {
			return di > dj
		}
		if di, dj := depth(pi), depth(pj); di != dj {
			return di < dj
		}
		if len(pi) != len(pj) {
			return len(pi) < len(pj)
		}
		return pi < pj
	})

	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	return cands
}

func tier(p string) int {
	switch {
	case isEntrypoint(p):
		return 0
	case isConfig(p):
		return 1
	default:
		return 2
	}
}

// entryScore ranks how strongly a path smells like a program entrypoint
// (lower is stronger). 3 means "not an entrypoint at all".
func entryScore(p string) int {
	base := strings.ToLower(path.Base(p))
	switch strings.TrimSuffix(base, path.Ext(base)) {
	case "main", "__main__":
		return 0
	case "index", "app", "server", "cli":
		return 1
	}
	if strings.HasPrefix(p, "cmd/") {
		return 2
	}
	return 3
}

func isEntrypoint(p string) bool { return entryScore(p) < 3 }

// configNames / configExts recognise the manifests that explain a project
// faster than any single source file.
var configNames = map[string]bool{
	"go.mod":           true,
	"package.json":     true,
	"makefile":         true,
	"dockerfile":       true,
	"cargo.toml":       true,
	"pyproject.toml":   true,
	"requirements.txt": true,
	"setup.py":         true,
	"setup.cfg":        true,
	"tsconfig.json":    true,
	"cmakelists.txt":   true,
	"build.gradle":     true,
	"pom.xml":          true,
	"gemfile":          true,
	"composer.json":    true,
	"readme.md":        true,
}

var configExts = map[string]bool{
	".toml": true,
	".yaml": true,
	".yml":  true,
	".ini":  true,
	".cfg":  true,
	".mod":  true,
}

func isConfig(p string) bool {
	base := strings.ToLower(path.Base(p))
	if configNames[base] {
		return true
	}
	return configExts[path.Ext(base)]
}
