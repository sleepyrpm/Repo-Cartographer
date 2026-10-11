package report

import (
	"path"
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

func TestRankStartersEmptyInput(t *testing.T) {
	got := RankStarters(Input{})
	if got == nil {
		t.Fatalf("RankStarters must return a non-nil slice")
	}
	if len(got) != 0 {
		t.Fatalf("want no starters, got %v", starterPaths(got))
	}
}

func TestRankStartersEntrypointFirst(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{
			name:  "go cmd main beats everything",
			files: []string{"README.md", "go.mod", "Makefile", "cmd/app/main.go", "internal/core/core.go"},
			want:  "cmd/app/main.go",
		},
		{
			name:  "plain go main",
			files: []string{"README.md", "go.mod", "main.go"},
			want:  "main.go",
		},
		{
			name:  "node index",
			files: []string{"package.json", "src/index.ts", "src/util.ts", "jest.config.js"},
			want:  "src/index.ts",
		},
		{
			name:  "python package main",
			files: []string{"pyproject.toml", "requirements.txt", "pkg/__main__.py", "pkg/util.py"},
			want:  "pkg/__main__.py",
		},
		{
			name:  "django manage",
			files: []string{"manage.py", "requirements.txt", "app/models.py"},
			want:  "manage.py",
		},
		{
			name:  "flask app",
			files: []string{"app.py", "requirements.txt", "Dockerfile"},
			want:  "app.py",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := Input{Files: repoFiles(tc.files...)}
			got := RankStarters(in)
			if len(got) == 0 {
				t.Fatalf("no starters for %v", tc.files)
			}
			if got[0].Path != tc.want {
				t.Fatalf("want %q first, got %v", tc.want, starterPaths(got))
			}
		})
	}
}

func repoFiles(paths ...string) []contracts.RepoFile {
	out := make([]contracts.RepoFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, contracts.RepoFile{Path: p, Lang: "Go", Size: 1000})
	}
	return out
}

func TestRankStartersHubIsHighlyRanked(t *testing.T) {
	in := sampleInput()
	got := starterPaths(RankStarters(in))
	if got[0] != "cmd/cartographer/main.go" {
		t.Fatalf("want entrypoint first, got %v", got)
	}
	hub := "internal/contracts/types.go"
	if i := indexOf(got, hub); i < 0 || i > 2 {
		t.Fatalf("want hub %q in the top 3, got %v", hub, got)
	}
}

func TestRankStartersRespectsMaxStarters(t *testing.T) {
	in := sampleInput()
	for _, max := range []int{1, 2, 3, 100} {
		in.MaxStarters = max
		got := RankStarters(in)
		want := max
		if want > 8 {
			want = 8 // 8 of the 9 sample files survive the heuristic (the test file does not)
		}
		if len(got) != want {
			t.Errorf("MaxStarters=%d: want %d findings, got %d (%v)", max, want, len(got), starterPaths(got))
		}
	}
	in.MaxStarters = 0
	if n := len(RankStarters(in)); n > DefaultMaxStarters {
		t.Errorf("MaxStarters<=0 must fall back to %d, got %d", DefaultMaxStarters, n)
	}
}

func TestRankStartersPenalisesTestsVendoredAndGenerated(t *testing.T) {
	in := Input{
		Files: repoFiles(
			"internal/core/core.go",
			"internal/core/core_test.go",
			"internal/core/testdata/fixture.go",
			"vendor/github.com/x/y/y.go",
			"internal/core/api.pb.go",
			"node_modules/left-pad/index.js",
		),
		Imports: map[string][]string{
			// Give the junk files the strongest possible structural signal;
			// the penalties must still bury them.
			"internal/core/core_test.go":        {"internal/core/core.go"},
			"vendor/github.com/x/y/y.go":        {"internal/core/core.go"},
			"internal/core/api.pb.go":           {"internal/core/core.go"},
			"node_modules/left-pad/index.js":    {"internal/core/core.go"},
			"internal/core/testdata/fixture.go": {"internal/core/core.go"},
		},
		MaxStarters: 10,
	}
	got := starterPaths(RankStarters(in))
	if len(got) == 0 || got[0] != "internal/core/core.go" {
		t.Fatalf("want the real source file first, got %v", got)
	}
	for _, junk := range []string{
		"internal/core/core_test.go",
		"internal/core/testdata/fixture.go",
		"vendor/github.com/x/y/y.go",
		"internal/core/api.pb.go",
		"node_modules/left-pad/index.js",
	} {
		if contains(got, junk) {
			t.Errorf("%q should have been penalised out of the starters, got %v", junk, got)
		}
	}
}

func TestRankStartersPenalisesVeryLargeFiles(t *testing.T) {
	in := Input{
		Files: []contracts.RepoFile{
			{Path: "pkg/small.go", Lang: "Go", Size: 1200},
			{Path: "pkg/huge.go", Lang: "Go", Size: 400_000},
		},
		Imports: map[string][]string{
			"pkg/a.go": {"pkg/small.go", "pkg/huge.go"},
			"pkg/b.go": {"pkg/small.go", "pkg/huge.go"},
		},
		MaxStarters: 5,
	}
	got := starterPaths(RankStarters(in))
	if indexOf(got, "pkg/small.go") >= indexOf(got, "pkg/huge.go") && contains(got, "pkg/huge.go") {
		t.Fatalf("the enormous file should rank below the small one, got %v", got)
	}
}

func TestRankStartersFoldsInAgentNotes(t *testing.T) {
	in := sampleInput()
	var found bool
	for _, f := range RankStarters(in) {
		if f.Path == "internal/agent/loop.go" {
			found = true
			if !strings.Contains(f.Note, "the observe-decide-act loop lives here") {
				t.Errorf("note not folded into reason: %q", f.Note)
			}
			if !strings.Contains(f.Note, "agent note:") {
				t.Errorf("reason should attribute the agent note: %q", f.Note)
			}
		}
	}
	if !found {
		t.Fatalf("expected the noted file among the starters: %v", starterPaths(RankStarters(in)))
	}
}

func TestRankStartersReasonsAreActionable(t *testing.T) {
	inputs := map[string]Input{
		"sample": sampleInput(),
		"bare":   {Files: repoFiles("pkg/a.go", "pkg/b.go", "deep/a/b/c/d.go"), MaxStarters: 5},
		"js":     {Files: repoFiles("src/index.ts", "package.json", "webpack.config.js", "Dockerfile", "README.md"), MaxStarters: 5},
	}
	for name, in := range inputs {
		for _, f := range RankStarters(in) {
			base := path.Base(f.Path)
			switch {
			case strings.TrimSpace(f.Note) == "":
				t.Errorf("%s: %s has an empty reason", name, f.Path)
			case f.Note == base || f.Note == f.Path:
				t.Errorf("%s: %s reason is just the filename: %q", name, f.Path, f.Note)
			case !strings.Contains(f.Note, " "):
				t.Errorf("%s: %s reason is a single token: %q", name, f.Path, f.Note)
			case len(f.Note) < 25:
				t.Errorf("%s: %s reason is too terse to act on: %q", name, f.Path, f.Note)
			}
		}
	}
}

func TestRankStartersDeduplicatesByPath(t *testing.T) {
	in := Input{
		Files: []contracts.RepoFile{
			{Path: "main.go", Lang: "Go"},
			{Path: "./main.go", Lang: "Go"},
			{Path: "main.go", Lang: "Go"},
			{Path: "README.md", Lang: "Markdown"},
		},
		MaxStarters: 5,
	}
	got := starterPaths(RankStarters(in))
	seen := map[string]bool{}
	for _, p := range got {
		if seen[p] {
			t.Fatalf("duplicate starter %q in %v", p, got)
		}
		seen[p] = true
	}
	if len(got) != 2 {
		t.Fatalf("want 2 unique starters, got %v", got)
	}
}

func TestRankStartersDeterministic(t *testing.T) {
	in := sampleInput()
	first := starterPaths(RankStarters(in))
	for i := 0; i < 25; i++ {
		got := starterPaths(RankStarters(in))
		if strings.Join(got, "|") != strings.Join(first, "|") {
			t.Fatalf("run %d differs: %v vs %v", i, got, first)
		}
	}
}
