package report

import (
	"strings"
	"testing"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

func TestBuildPopulatesEveryField(t *testing.T) {
	in := sampleInput()
	r := Build(in)

	if r.RootPath != in.RootPath {
		t.Errorf("RootPath = %q, want %q", r.RootPath, in.RootPath)
	}
	if !strings.HasPrefix(r.Diagram, headerComment) {
		t.Errorf("Diagram missing header:\n%s", r.Diagram)
	}
	if strings.Contains(r.Diagram, "```") {
		t.Errorf("Diagram must be fence-free:\n%s", r.Diagram)
	}
	if len(r.Starters) == 0 {
		t.Errorf("want starters")
	}
	if r.Summary == "" {
		t.Errorf("want a summary")
	}
	want := []string{"README.md", "cmd/cartographer/main.go", "internal/agent/loop.go"}
	if strings.Join(r.Visited, "|") != strings.Join(want, "|") {
		t.Errorf("Visited = %v, want %v", r.Visited, want)
	}
}

func TestBuildVisitedIsDedupedAndNormalised(t *testing.T) {
	in := Input{Visited: []string{"a.go", "./a.go", "", "   ", "b/c.go", "a.go"}}
	got := Build(in).Visited
	if strings.Join(got, "|") != "a.go|b/c.go" {
		t.Fatalf("Visited = %v", got)
	}
}

func TestBuildEmptyInput(t *testing.T) {
	r := Build(Input{})
	if r.Diagram == "" {
		t.Errorf("even empty input must yield a renderable diagram")
	}
	if len(r.Starters) != 0 {
		t.Errorf("want no starters, got %v", starterPaths(r.Starters))
	}
	if len(r.Visited) != 0 {
		t.Errorf("want no visited files, got %v", r.Visited)
	}
	if !strings.Contains(r.Summary, "0 files") {
		t.Errorf("summary should state the empty inventory: %q", r.Summary)
	}
}

func TestBuildDeterministic(t *testing.T) {
	in := sampleInput()
	a, b := Build(in), Build(in)
	if Markdown(a) != Markdown(b) {
		t.Fatalf("Build is not deterministic:\n--- a ---\n%s\n--- b ---\n%s", Markdown(a), Markdown(b))
	}
	if a.Summary != b.Summary || a.Diagram != b.Diagram {
		t.Fatalf("Build fields differ between runs")
	}
}

func TestBuildDeterministicAcrossMapInsertionOrder(t *testing.T) {
	base := sampleInput()

	reordered := make(map[string][]string, len(base.Imports))
	keys := []string{}
	for k := range base.Imports {
		keys = append(keys, k)
	}
	for i := len(keys) - 1; i >= 0; i-- {
		reordered[keys[i]] = base.Imports[keys[i]]
	}
	other := base
	other.Imports = reordered

	if Markdown(Build(base)) != Markdown(Build(other)) {
		t.Fatalf("Build output depends on map insertion order")
	}
}

func TestSummaryStatesTheFacts(t *testing.T) {
	in := sampleInput()
	got := summarize(in)

	for _, want := range []string{
		"repo-cartographer",              // repo basename
		"9 files",                        // file count
		"Go 7",                           // language mix
		"Markdown 1",                     // language mix
		"It read 3 files",                // files the agent read
		"8 import edges",                 // resolved edges
		"11 of its 20",                   // steps vs budget
		"the model reported it was done", // stop reason
		"4.3s",                           // elapsed
		"`qwen2.5:1.5b`",                 // the model that drove it
		"Ollama",
		"1 note",
		"2 searches",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestSummaryDegradesWithoutModelOrBudget(t *testing.T) {
	got := summarize(Input{
		RootPath:  "/x/y/demo",
		Files:     []contracts.RepoFile{{Path: "a.go"}},
		StepsUsed: 3,
	})
	if !strings.Contains(got, "demo") {
		t.Errorf("want repo basename: %q", got)
	}
	if !strings.Contains(got, "3 steps") {
		t.Errorf("want bare step count when no budget: %q", got)
	}
	if !strings.Contains(got, "a local open model") {
		t.Errorf("want generic model phrasing: %q", got)
	}
	if strings.Contains(got, "The run took") {
		t.Errorf("zero duration should be omitted: %q", got)
	}
}

func TestSummaryLanguageMixTopThree(t *testing.T) {
	in := Input{Files: []contracts.RepoFile{
		{Path: "a.go", Lang: "Go"}, {Path: "b.go", Lang: "Go"}, {Path: "c.go", Lang: "Go"},
		{Path: "a.py", Lang: "Python"}, {Path: "b.py", Lang: "Python"},
		{Path: "a.md", Lang: "Markdown"},
		{Path: "a.rs", Lang: "Rust"},
	}}
	got := summarize(in)
	if !strings.Contains(got, "Go 3, Python 2, Markdown 1") {
		t.Errorf("want the top three languages in count order: %q", got)
	}
	if strings.Contains(got, "Rust") {
		t.Errorf("fourth language should be omitted: %q", got)
	}
}

func TestHumanDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{1500 * time.Microsecond, "2ms"},
		{4250 * time.Millisecond, "4.3s"},
		{95 * time.Second, "1m35s"},
	}
	for _, tc := range tests {
		if got := humanDuration(tc.d); got != tc.want {
			t.Errorf("humanDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestRootName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/home/dev/repo-cartographer", "repo-cartographer"},
		{"/home/dev/repo-cartographer/", "repo-cartographer"},
		{"", "this repository"},
		{"   ", "this repository"},
		{".", "this repository"},
		{"relative/dir", "dir"},
	}
	for _, tc := range tests {
		if got := rootName(tc.in); got != tc.want {
			t.Errorf("rootName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
