package report

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// Score weights. They are deliberately spread far apart so the ordering of the
// categories is obvious from the numbers alone: an entrypoint always outranks a
// hub, which outranks a manifest, which outranks an ordinary source file.
const (
	wEntrypointCmdMain = 110 // cmd/**/main.go: the binary a user actually runs
	wEntrypointMain    = 100 // main.{go,py,rs,...} anywhere else
	wEntrypointModule  = 95  // __main__.py
	wEntrypointApp     = 90  // index.*, app.*, manage.py
	wEntrypointServer  = 85  // server.*
	wReadme            = 55  // README* is a strong early read
	wMakefile          = 60  // Makefile targets are the project's real commands
	wManifest          = 40  // go.mod, package.json, Cargo.toml, pyproject.toml
	wLockish           = 35  // requirements.txt
	wContainer         = 38  // Dockerfile
	wCompose           = 36  // docker-compose.*
	wCI                = 30  // .github/workflows/*, .gitlab-ci.yml, ...
	wToolConfig        = 28  // *.config.*
	wHubMax            = 45  // full marks for the most-imported file in the repo
	wVisitedBonus      = 8   // the agent itself chose to open this file
	wNoteBonus         = 6   // the agent wrote something down about it
	pTest              = 70  // tests/fixtures are rarely the place to start
	pVendored          = 200 // vendored/third-party trees are never where you start
	pGenerated         = 50  // generated code explains nothing about intent
	pBig               = 15  // > 50 KB
	pHuge              = 35  // > 200 KB
	bigBytes           = 50_000
	hugeBytes          = 200_000
)

// RankStarters returns up to MaxStarters files a newcomer should read first,
// best first, each with a specific one-line reason built from the evidence the
// agent actually gathered. The returned slice is never nil.
func RankStarters(in Input) []contracts.Finding {
	files := uniqueFiles(in.Files)
	out := make([]contracts.Finding, 0, in.maxStarters())
	if len(files) == 0 {
		return out
	}

	set := nodeSet(in)
	inDeg, _ := degrees(resolvedEdges(in, set))
	notes := noteIndex(in.Notes)
	visited := make(map[string]bool)
	for _, v := range dedupStrings(in.Visited) {
		visited[v] = true
	}

	// Normalise the hub signal against the busiest file in this repo, so that
	// "most imported" means something in a small repo as well as a large one.
	maxIn := 0
	for _, f := range files {
		if d := inDeg[f.Path]; d > maxIn {
			maxIn = d
		}
	}

	type candidate struct {
		path   string
		score  int
		inDeg  int
		depth  int
		reason string
	}

	ranked := make([]candidate, 0, len(files))
	for _, f := range files {
		score, clauses := scoreFile(f, inDeg[f.Path], maxIn, visited[f.Path], notes[f.Path] != "")
		// Everything is penalised, nothing is hidden: a file only drops out of
		// the running if its penalties cancel every positive signal it had.
		if score <= 0 {
			continue
		}
		ranked = append(ranked, candidate{
			path:   f.Path,
			score:  score,
			inDeg:  inDeg[f.Path],
			depth:  depthOf(f.Path),
			reason: composeReason(clauses, notes[f.Path]),
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.inDeg != b.inDeg {
			return a.inDeg > b.inDeg
		}
		if a.depth != b.depth {
			return a.depth < b.depth // shallower paths read first
		}
		return a.path < b.path
	})

	limit := in.maxStarters()
	for _, c := range ranked {
		if len(out) >= limit {
			break
		}
		out = append(out, contracts.Finding{Path: c.path, Note: c.reason})
	}
	return out
}

// scoreFile applies the heuristic and collects the evidence clauses that
// justify the score, strongest signal first.
func scoreFile(f contracts.RepoFile, inDeg, maxIn int, visited, hasNote bool) (int, []string) {
	p := f.Path
	base := strings.ToLower(path.Base(p))
	score := 0
	var clauses []string

	// 1. Entrypoints: the single strongest signal. Start where the program starts.
	if pts, why := entrypointSignal(p, base); pts > 0 {
		score += pts
		clauses = append(clauses, why)
	}

	// 2. Docs: the README is the author's own orientation for newcomers.
	if strings.HasPrefix(base, "readme") {
		score += wReadme
		clauses = append(clauses, "the project README — the author's own description of what this is and how to run it")
	}

	// 3. Config/manifests: useful, but below entrypoints and hubs.
	if pts, why := manifestSignal(p, base); pts > 0 {
		score += pts
		clauses = append(clauses, why)
	}

	// 4. In-degree: the most-imported file is the most load-bearing one.
	if inDeg > 0 {
		if maxIn > 0 {
			score += wHubMax * inDeg / maxIn
		}
		clauses = append(clauses, hubClause(inDeg))
	}

	// 5. Small nudges from what the agent chose to do.
	if visited {
		score += wVisitedBonus
		clauses = append(clauses, "the agent opened this file while exploring, so it is on the trail it followed")
	}
	if hasNote {
		score += wNoteBonus
	}

	// 6. A shallow path is a mild positive, and also keeps plain source files
	// in the running when nothing else distinguishes them.
	if d := depthOf(p); d < 6 {
		score += 6 - d
	} else {
		score += 1
	}

	// 7. Penalties: tests, vendored trees, generated code, enormous files.
	if isTestPath(p, base) {
		score -= pTest
	}
	if isVendored(p) {
		score -= pVendored
	}
	if isGenerated(p, base) {
		score -= pGenerated
	}
	switch {
	case f.Size > hugeBytes:
		score -= pHuge
	case f.Size > bigBytes:
		score -= pBig
	}

	if len(clauses) == 0 {
		clauses = append(clauses, fallbackClause(p))
	}
	return score, clauses
}

// entrypointSignal recognises the conventional "the program starts here" files.
func entrypointSignal(p, base string) (int, string) {
	stem := base
	if i := strings.LastIndex(base, "."); i > 0 {
		stem = base[:i]
	}
	switch {
	case stem == "main" && strings.HasPrefix(p, "cmd/"):
		return wEntrypointCmdMain, "the CLI entrypoint under cmd/ — it parses the flags and starts the run, so it names every moving part"
	case stem == "main":
		return wEntrypointMain, "program entrypoint — the main function that wires the pieces together and starts them"
	case base == "__main__.py":
		return wEntrypointModule, "module entrypoint — this is what runs when the package is executed directly"
	case base == "manage.py":
		return wEntrypointApp, "Django management entrypoint — every admin, migrate and runserver command goes through it"
	case stem == "index" && isScript(base):
		return wEntrypointApp, "package entrypoint — the module every consumer loads first, so it maps the public surface"
	case stem == "app" && (isScript(base) || strings.HasSuffix(base, ".py")):
		return wEntrypointApp, "application entrypoint — it builds the app object and registers everything on it"
	case stem == "server" && (isScript(base) || strings.HasSuffix(base, ".py") || strings.HasSuffix(base, ".go")):
		return wEntrypointServer, "server entrypoint — it sets up the routes and starts listening, so request flow begins here"
	case base == "makefile" || base == "gnumakefile":
		return wMakefile, "the Makefile — its targets are the commands this project is actually meant to be driven with"
	}
	return 0, ""
}

// isScript reports whether base is a JS/TS-family source file.
func isScript(base string) bool {
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// manifestSignal recognises build manifests, container and CI configuration.
func manifestSignal(p, base string) (int, string) {
	switch base {
	case "go.mod":
		return wManifest, "declares the Go module and the exact dependencies it is allowed to use"
	case "package.json":
		return wManifest, "declares the package, its runnable scripts and its dependencies"
	case "cargo.toml":
		return wManifest, "declares the crate, its features and its dependencies"
	case "pyproject.toml":
		return wManifest, "declares the Python project, its build backend and its dependencies"
	case "requirements.txt":
		return wLockish, "pins the Python dependencies the project installs before it will run"
	case "gemfile", "build.gradle", "pom.xml", "cmakelists.txt":
		return wManifest, "the build manifest — it declares how this project is assembled and what it depends on"
	case ".gitlab-ci.yml", ".travis.yml", "azure-pipelines.yml", "jenkinsfile":
		return wCI, "the CI pipeline — the authoritative list of how this project is built, tested and released"
	}
	switch {
	case strings.HasPrefix(base, "dockerfile"):
		return wContainer, "defines how the project is built and run inside a container, dependencies included"
	case strings.HasPrefix(base, "docker-compose"):
		return wCompose, "describes the services this project expects to run alongside it locally"
	case strings.HasPrefix(p, ".github/workflows/"), strings.HasPrefix(p, ".circleci/"):
		return wCI, "the CI pipeline — the authoritative list of how this project is built, tested and released"
	case strings.Contains(base, ".config."):
		return wToolConfig, "tooling configuration — it shapes how the sources are compiled, bundled or linted"
	}
	return 0, ""
}

// hubClause phrases the in-degree evidence.
func hubClause(inDeg int) string {
	if inDeg == 1 {
		return "imported by one other file in the repo — a shared piece rather than a leaf"
	}
	return fmt.Sprintf("imported by %d other files — a load-bearing abstraction much of the code depends on", inDeg)
}

// fallbackClause is used when a file carries no categorical signal at all. It
// must still tell the reader something actionable: never just the filename.
func fallbackClause(p string) string {
	d := path.Dir(p)
	if d == "." {
		return "a source file at the repository root — part of the main implementation"
	}
	return fmt.Sprintf("part of the %s package — read it for the implementation details that live there", d)
}

// composeReason joins at most two evidence clauses into one line and folds in
// the agent's own note for the path when it has one.
func composeReason(clauses []string, note string) string {
	if len(clauses) > 2 {
		clauses = clauses[:2]
	}
	reason := strings.Join(clauses, "; ")
	if note = strings.TrimSpace(note); note != "" {
		reason += " — agent note: " + strings.TrimSuffix(note, ".")
	}
	return reason
}

// ---------------------------------------------------------------------------
// Penalty predicates
// ---------------------------------------------------------------------------

func isTestPath(p, base string) bool {
	lower := strings.ToLower(p)
	for _, seg := range []string{"/test/", "/tests/", "/__tests__/", "/testdata/", "/fixtures/", "/spec/"} {
		if strings.Contains("/"+lower, seg) {
			return true
		}
	}
	if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") {
		return true
	}
	for _, suf := range []string{".test.js", ".test.ts", ".test.tsx", ".spec.js", ".spec.ts", ".spec.tsx", "_test.py", "_spec.rb"} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

func isVendored(p string) bool {
	lower := strings.ToLower("/" + p)
	for _, seg := range []string{"/vendor/", "/node_modules/", "/third_party/", "/thirdparty/", "/.venv/", "/site-packages/"} {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	return false
}

func isGenerated(p, base string) bool {
	lower := strings.ToLower(p)
	if strings.Contains(lower, "/generated/") || strings.HasPrefix(lower, "generated/") {
		return true
	}
	for _, suf := range []string{".pb.go", ".pb.gw.go", "_generated.go", ".gen.go", ".generated.ts", "_pb2.py", ".min.js"} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}
