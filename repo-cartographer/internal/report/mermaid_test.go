package report

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

func TestMermaidAlwaysValidShape(t *testing.T) {
	tests := []struct {
		name string
		in   Input
	}{
		{"empty", Input{}},
		{"only root path", Input{RootPath: "/tmp/x"}},
		{"files but no imports", Input{Files: []contracts.RepoFile{{Path: "a.go"}, {Path: "b/c.go"}}}},
		{"sample", sampleInput()},
		{"imports only", Input{Imports: map[string][]string{"a.go": {"b.go"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Mermaid(tc.in)
			if !strings.HasPrefix(got, headerComment+"\n") {
				t.Fatalf("missing generator comment header:\n%s", got)
			}
			lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
			if len(lines) < 2 || lines[1] != "graph LR" {
				t.Fatalf("second line must be %q, got %q", "graph LR", lines[1])
			}
			if strings.Contains(got, "```") {
				t.Fatalf("diagram must not contain fences:\n%s", got)
			}
			if !strings.Contains(got, `["`) {
				t.Fatalf("diagram must always declare at least one node:\n%s", got)
			}
			// Every subgraph must be closed.
			if open, closed := strings.Count(got, "subgraph "), countEndLines(got); open != closed {
				t.Fatalf("%d subgraphs but %d end lines:\n%s", open, closed, got)
			}
		})
	}
}

func countEndLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "end" {
			n++
		}
	}
	return n
}

func TestMermaidEmptyInputPlaceholder(t *testing.T) {
	got := Mermaid(Input{})
	if !strings.Contains(got, `empty["no files discovered"]`) {
		t.Fatalf("expected placeholder node, got:\n%s", got)
	}
	if strings.Contains(got, "subgraph") {
		t.Fatalf("empty diagram should have no subgraphs:\n%s", got)
	}
	if strings.Contains(got, "-->") {
		t.Fatalf("empty diagram should have no edges:\n%s", got)
	}
}

func TestMermaidNodeIDSanitisationAndLabelEscaping(t *testing.T) {
	in := Input{
		Files: []contracts.RepoFile{
			{Path: `weird dir/a-b.c"d".go`},
			{Path: `./dots/../x.y-z.go`},
			{Path: `has[brackets].md`},
			{Path: `plain.go`},
		},
	}
	got := Mermaid(in)

	nodes := declaredNodes(t, got)
	if len(nodes) != 4 {
		t.Fatalf("want 4 nodes, got %d:\n%s", len(nodes), got)
	}
	// IDs are checked by declaredNodes; now check label escaping.
	all := strings.Join(valuesOf(nodes), "\n")
	if !strings.Contains(all, `weird dir/a-b.c&quot;d&quot;.go`) {
		t.Errorf("quotes in label not escaped:\n%s", all)
	}
	if !strings.Contains(all, `has&#91;brackets&#93;.md`) {
		t.Errorf("brackets in label not escaped:\n%s", all)
	}
	if !strings.Contains(all, `x.y-z.go`) {
		t.Errorf("path not cleaned to x.y-z.go:\n%s", all)
	}
	// No raw path may ever be used as an identifier.
	for _, line := range strings.Split(got, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "%%") || strings.HasPrefix(trimmed, "classDef") || strings.HasPrefix(trimmed, "class ") {
			continue
		}
		if i := strings.Index(trimmed, "["); i > 0 {
			if !idOK.MatchString(trimmed[:i]) && !strings.HasPrefix(trimmed[:i], "subgraph sg") && trimmed[:i] != "empty" {
				t.Errorf("unsanitised identifier %q in line %q", trimmed[:i], trimmed)
			}
		}
	}
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestMermaidSubgraphGrouping(t *testing.T) {
	got := Mermaid(sampleInput())
	groups := subgraphRe.FindAllStringSubmatch(got, -1)
	if len(groups) == 0 {
		t.Fatalf("no subgraphs emitted:\n%s", got)
	}
	titles := map[string]bool{}
	for _, g := range groups {
		titles[strings.Trim(g[2], `"`)] = true
	}
	for _, want := range []string{rootGroup, "cmd/cartographer", "internal/agent", "internal/tools", "internal/contracts", "internal/report"} {
		if !titles[want] {
			t.Errorf("missing subgraph %q; got %v", want, titles)
		}
	}
	// Root-level files must live in the "(root)" group.
	idx := strings.Index(got, `subgraph sg1["(root)"]`)
	if idx < 0 {
		t.Fatalf("expected (root) to be the first subgraph:\n%s", got)
	}
	ids := labelToID(t, got)
	if !strings.Contains(got[idx:], "    "+ids["go.mod"]+"[") {
		t.Errorf("go.mod not declared inside the (root) subgraph:\n%s", got)
	}
}

func TestMermaidEdges(t *testing.T) {
	in := Input{
		Files: []contracts.RepoFile{{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}},
		Imports: map[string][]string{
			"a.go": {
				"b.go",     // normal
				"b.go",     // duplicate -> deduped
				"./b.go",   // same file after normalisation -> deduped
				"a.go",     // self-edge -> dropped
				"fmt",      // external spec, not a file -> dropped
				"zzz.go",   // not in Files but looks like a repo file -> kept as a node
				"",         // junk -> dropped
				"   ",      // junk -> dropped
				"nope/dir", // no extension -> dropped
			},
			"ghost.go": {"a.go"}, // source not in Files but file-like -> kept
			"fmt":      {"a.go"}, // external source -> dropped
		},
	}
	got := Mermaid(in)
	ids := labelToID(t, got)

	if _, ok := ids["zzz.go"]; !ok {
		t.Errorf("imported repo file missing from node set:\n%s", got)
	}
	if _, ok := ids["fmt"]; ok {
		t.Errorf("external spec %q must not become a node:\n%s", "fmt", got)
	}
	if _, ok := ids["nope/dir"]; ok {
		t.Errorf("extension-less target must not become a node:\n%s", got)
	}

	edges := diagramEdges(got)
	want := map[[2]string]bool{
		{ids["a.go"], ids["b.go"]}:     true,
		{ids["a.go"], ids["zzz.go"]}:   true,
		{ids["ghost.go"], ids["a.go"]}: true,
	}
	if len(edges) != len(want) {
		t.Fatalf("want %d edges, got %d (%v):\n%s", len(want), len(edges), edges, got)
	}
	seen := map[[2]string]bool{}
	for _, e := range edges {
		if seen[e] {
			t.Errorf("duplicate edge %v", e)
		}
		seen[e] = true
		if e[0] == e[1] {
			t.Errorf("self-edge %v emitted", e)
		}
		if !want[e] {
			t.Errorf("unexpected edge %v in:\n%s", e, got)
		}
	}
}

func TestMermaidHighlightsStartersAndVisited(t *testing.T) {
	in := sampleInput()
	got := Mermaid(in)

	for _, def := range []string{classDefStarter, classDefVisited, classDefPlain} {
		if !strings.Contains(got, def) {
			t.Errorf("missing %q", def)
		}
	}

	ids := labelToID(t, got)
	starterID := ids[RankStarters(in)[0].Path]
	starterLine := classLine(got, classStarter)
	if !contains(strings.Split(starterLine, ","), starterID) {
		t.Errorf("top starter %s (%s) not in starter class line %q", RankStarters(in)[0].Path, starterID, starterLine)
	}

	// internal/agent/loop.go is visited; it is also a starter candidate, so
	// pick a visited-only file to assert the visited class is used at all.
	visitedLine := classLine(got, classVisited)
	plainLine := classLine(got, classPlain)
	if visitedLine == "" && plainLine == "" {
		t.Errorf("expected at least one visited/plain class line:\n%s", got)
	}
	// A node may only be in one class.
	counts := map[string]int{}
	for _, line := range []string{starterLine, visitedLine, plainLine} {
		for _, id := range strings.Split(line, ",") {
			if id = strings.TrimSpace(id); id != "" {
				counts[id]++
			}
		}
	}
	for id, n := range counts {
		if n > 1 {
			t.Errorf("node %s assigned %d classes", id, n)
		}
	}
	if len(counts) != len(declaredNodes(t, got)) {
		t.Errorf("classed %d nodes but declared %d", len(counts), len(declaredNodes(t, got)))
	}
}

// classLine returns the comma list from the `class a,b name` line for a class.
func classLine(diagram, class string) string {
	for _, line := range strings.Split(diagram, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "class ") && strings.HasSuffix(line, " "+class) {
			return strings.TrimSuffix(strings.TrimPrefix(line, "class "), " "+class)
		}
	}
	return ""
}

func TestMermaidTruncationKeepsStartersAndReportsOmitted(t *testing.T) {
	in := sampleInput()
	in.MaxNodes = 3

	starters := RankStarters(in)
	got := Mermaid(in)

	nodes := declaredNodes(t, got)
	if len(nodes) != 3 {
		t.Fatalf("want 3 nodes under MaxNodes=3, got %d:\n%s", len(nodes), got)
	}

	total := len(nodeSet(in))
	wantComment := fmt.Sprintf("%%%% %d more files omitted", total-3)
	if !strings.Contains(got, wantComment) {
		t.Errorf("missing %q in:\n%s", wantComment, got)
	}

	ids := labelToID(t, got)
	for i := 0; i < 3 && i < len(starters); i++ {
		if _, ok := ids[starters[i].Path]; !ok {
			t.Errorf("truncation dropped starter #%d %q:\n%s", i+1, starters[i].Path, got)
		}
	}
	// Edges to truncated nodes must be gone.
	for _, e := range diagramEdges(got) {
		if _, ok := nodes[e[0]]; !ok {
			t.Errorf("edge from undeclared node %s", e[0])
		}
		if _, ok := nodes[e[1]]; !ok {
			t.Errorf("edge to undeclared node %s", e[1])
		}
	}
}

func TestMermaidNoTruncationCommentWhenUnderCap(t *testing.T) {
	if got := Mermaid(sampleInput()); strings.Contains(got, "more files omitted") {
		t.Errorf("unexpected truncation comment:\n%s", got)
	}
}

func TestMermaidDeterministic(t *testing.T) {
	in := sampleInput()
	first := Mermaid(in)
	for i := 0; i < 25; i++ {
		if got := Mermaid(in); got != first {
			t.Fatalf("Mermaid not deterministic on run %d:\n--- first ---\n%s\n--- got ---\n%s", i, first, got)
		}
	}
}

func TestMermaidDeterministicAcrossMapInsertionOrder(t *testing.T) {
	base := sampleInput()

	// Rebuild the import map with the keys inserted in the opposite order and
	// the targets reversed; the rendered diagram must be byte-identical.
	keys := make([]string, 0, len(base.Imports))
	for k := range base.Imports {
		keys = append(keys, k)
	}
	shuffled := make(map[string][]string, len(keys))
	for i := len(keys) - 1; i >= 0; i-- {
		src := base.Imports[keys[i]]
		rev := make([]string, len(src))
		for j := range src {
			rev[j] = src[len(src)-1-j]
		}
		shuffled[keys[i]] = rev
	}

	other := base
	other.Imports = shuffled
	// Reversing the inventory order must not matter either.
	files := make([]contracts.RepoFile, len(base.Files))
	for i := range base.Files {
		files[i] = base.Files[len(base.Files)-1-i]
	}
	other.Files = files

	if a, b := Mermaid(base), Mermaid(other); a != b {
		t.Fatalf("diagram depends on map/slice order:\n--- a ---\n%s\n--- b ---\n%s", a, b)
	}
}
