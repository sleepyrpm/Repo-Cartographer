package report

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// sampleInput is the shared fixture: a small, plausible Go repo with an
// entrypoint, a hub, a manifest, a README and a test file.
func sampleInput() Input {
	return Input{
		RootPath: "/home/dev/repo-cartographer",
		Files: []contracts.RepoFile{
			{Path: "README.md", Lang: "Markdown", Size: 2048},
			{Path: "go.mod", Lang: "Go module", Size: 64},
			{Path: "cmd/cartographer/main.go", Lang: "Go", Size: 3200},
			{Path: "internal/agent/loop.go", Lang: "Go", Size: 5400},
			{Path: "internal/agent/loop_test.go", Lang: "Go", Size: 4100},
			{Path: "internal/contracts/types.go", Lang: "Go", Size: 4800},
			{Path: "internal/tools/read.go", Lang: "Go", Size: 2600},
			{Path: "internal/tools/search.go", Lang: "Go", Size: 2700},
			{Path: "internal/report/markdown.go", Lang: "Go", Size: 3100},
		},
		Imports: map[string][]string{
			"cmd/cartographer/main.go": {
				"internal/agent/loop.go",
				"internal/contracts/types.go",
				"internal/report/markdown.go",
			},
			"internal/agent/loop.go":      {"internal/contracts/types.go", "internal/tools/read.go"},
			"internal/tools/read.go":      {"internal/contracts/types.go"},
			"internal/tools/search.go":    {"internal/contracts/types.go"},
			"internal/report/markdown.go": {"internal/contracts/types.go"},
		},
		RawImports: map[string][]string{
			"cmd/cartographer/main.go": {"flag", "fmt", "github.com/ayush-2007-iit/repo-cartographer/internal/agent"},
		},
		Visited:  []string{"README.md", "cmd/cartographer/main.go", "internal/agent/loop.go"},
		Notes:    []contracts.Finding{{Path: "internal/agent/loop.go", Note: "the observe-decide-act loop lives here"}},
		Searches: []string{"func main", "Decider"},

		StepsUsed:      11,
		Budget:         20,
		StoppedBecause: "the model reported it was done",
		ModelName:      "qwen2.5:1.5b",
		Duration:       4250 * time.Millisecond,
	}
}

var (
	nodeDeclRe = regexp.MustCompile(`(?m)^\s{2,}(n\d+)\[(".*")\]\s*$`)
	idOK       = regexp.MustCompile(`^n\d+$`)
	edgeRe     = regexp.MustCompile(`(?m)^\s+(\S+) --> (\S+)\s*$`)
	subgraphRe = regexp.MustCompile(`(?m)^\s+subgraph (sg\d+)\[("[^"]*")\]\s*$`)
)

// declaredNodes maps node ID -> label (including the surrounding quotes).
func declaredNodes(t *testing.T, diagram string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range nodeDeclRe.FindAllStringSubmatch(diagram, -1) {
		if !idOK.MatchString(m[1]) {
			t.Fatalf("node ID %q is not a sanitised nN identifier", m[1])
		}
		out[m[1]] = m[2]
	}
	return out
}

// labelToID inverts declaredNodes, stripping the quotes from the label.
func labelToID(t *testing.T, diagram string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for id, label := range declaredNodes(t, diagram) {
		out[strings.Trim(label, `"`)] = id
	}
	return out
}

// diagramEdges returns the "a --> b" pairs in the diagram.
func diagramEdges(diagram string) [][2]string {
	var out [][2]string
	for _, m := range edgeRe.FindAllStringSubmatch(diagram, -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

func starterPaths(fs []contracts.Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Path)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func indexOf(hay []string, needle string) int {
	for i, h := range hay {
		if h == needle {
			return i
		}
	}
	return -1
}
