package report

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// modelSentinel is the exact phrase Summary uses to name the model that drove
// the loop. Markdown recovers the model name from it, because contracts.Report
// carries no model field of its own.
const modelSentinel = "the local open model `"

// Build composes the finished report: the Mermaid diagram, the ranked starter
// files, a factual summary and the list of files the agent read.
//
// Build is deterministic for a given Input.
func Build(in Input) contracts.Report {
	return contracts.Report{
		RootPath: in.RootPath,
		Diagram:  Mermaid(in),
		Starters: RankStarters(in),
		Summary:  summarize(in),
		Visited:  dedupStrings(in.Visited),
	}
}

// summarize states what the agent did, in facts drawn only from Input.
func summarize(in Input) string {
	files := uniqueFiles(in.Files)
	set := nodeSet(in)
	edges := resolvedEdges(in, set)
	visited := dedupStrings(in.Visited)

	var b strings.Builder

	fmt.Fprintf(&b, "The agent explored %s and catalogued %s.", rootName(in.RootPath), plural(len(files), "file", "files"))
	if mix := languageMix(files, 3); mix != "" {
		fmt.Fprintf(&b, " By file count the mix is %s.", mix)
	}

	fmt.Fprintf(&b, " It read %s and resolved %s between files",
		plural(len(visited), "file", "files"),
		plural(len(edges), "import edge", "import edges"))
	if n := len(dedupStrings2(in.Searches)); n > 0 {
		fmt.Fprintf(&b, ", after running %s", plural(n, "search", "searches"))
	}
	b.WriteString(".")

	if n := len(noteIndex(in.Notes)); n > 0 {
		fmt.Fprintf(&b, " It recorded %s along the way.", plural(n, "note", "notes"))
	}

	if in.Budget > 0 {
		fmt.Fprintf(&b, " The loop used %d of its %d allowed steps", in.StepsUsed, in.Budget)
	} else {
		fmt.Fprintf(&b, " The loop used %s", plural(in.StepsUsed, "step", "steps"))
	}
	if why := strings.TrimRight(strings.TrimSpace(in.StoppedBecause), "."); why != "" {
		fmt.Fprintf(&b, " and stopped because %s", why)
	}
	b.WriteString(".")

	if in.Duration > 0 {
		fmt.Fprintf(&b, " The run took %s.", humanDuration(in.Duration))
	}

	if m := strings.TrimSpace(in.ModelName); m != "" {
		fmt.Fprintf(&b, " Every decision was made by %s%s` running under Ollama, with no cloud API calls.", modelSentinel, m)
	} else {
		b.WriteString(" Every decision was made by a local open model running under Ollama, with no cloud API calls.")
	}

	return b.String()
}

// rootName is the repo basename, for prose.
func rootName(root string) string {
	r := strings.TrimRight(strings.TrimSpace(root), `/\`)
	if r == "" {
		return "this repository"
	}
	base := filepath.Base(r)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "this repository"
	}
	return base
}

// languageMix renders the top n languages by file count, e.g. "Go 30, Markdown 4".
func languageMix(files []contracts.RepoFile, n int) string {
	counts := map[string]int{}
	for _, f := range files {
		if lang := strings.TrimSpace(f.Lang); lang != "" {
			counts[lang]++
		}
	}
	if len(counts) == 0 {
		return ""
	}
	langs := make([]string, 0, len(counts))
	for l := range counts {
		langs = append(langs, l)
	}
	sort.Slice(langs, func(i, j int) bool {
		if counts[langs[i]] != counts[langs[j]] {
			return counts[langs[i]] > counts[langs[j]]
		}
		return langs[i] < langs[j]
	})
	if len(langs) > n {
		langs = langs[:n]
	}
	parts := make([]string, 0, len(langs))
	for _, l := range langs {
		parts = append(parts, fmt.Sprintf("%s %d", l, counts[l]))
	}
	return strings.Join(parts, ", ")
}

// plural formats a count with the right noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// dedupStrings2 dedupes free-form strings (search patterns), which are not paths.
func dedupStrings2(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// humanDuration renders a duration at a readable, stable precision.
func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Minute:
		return d.Round(time.Second).String()
	case d >= time.Second:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Millisecond).String()
	}
}
