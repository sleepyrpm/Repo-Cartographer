package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/agent"
	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
	"github.com/ayush-2007-iit/repo-cartographer/internal/model"
	"github.com/ayush-2007-iit/repo-cartographer/internal/report"
	"github.com/ayush-2007-iit/repo-cartographer/internal/tools"
)

// fixtureRoot locates testdata/sample-repo relative to this test file and skips
// the test with a clear message when it is missing.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "sample-repo"))
	if err != nil {
		t.Fatalf("resolving fixture path: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Skipf("fixture repo not found at %s (expected testdata/sample-repo); skipping", root)
	}
	return root
}

// ---------------------------------------------------------------------------
// The stub brain: no network, no Ollama, fully deterministic.
// ---------------------------------------------------------------------------

// scriptedDecider stands in for internal/model. It walks a fixed plan that
// exercises every tool, deliberately starts with a bogus tool name to prove the
// loop survives a bad decision, and then reads the unread files it is offered
// before declaring itself done.
type scriptedDecider struct {
	calls  int
	reads  int
	script []contracts.Decision
}

func newScriptedDecider() *scriptedDecider {
	return &scriptedDecider{script: []contracts.Decision{
		// Step 1 is garbage on purpose: an unknown tool with nonsense args.
		{Tool: "frobnicate_repo", Args: map[string]any{"path": 42}, Reason: "deliberately invalid tool"},
		{Tool: contracts.ToolRepoTree, Args: map[string]any{}, Reason: "get the lay of the land"},
		{Tool: contracts.ToolReadFile, Args: map[string]any{"path": "app/main.py"}, Reason: "looks like the entrypoint"},
		{Tool: contracts.ToolFollowImports, Args: map[string]any{"path": "app/main.py"}, Reason: "see what the entrypoint pulls in"},
		{Tool: contracts.ToolReadFile, Args: map[string]any{"path": "app/core.py"}, Reason: "main.py imports it"},
		{Tool: contracts.ToolFollowImports, Args: map[string]any{"path": "app/core.py"}, Reason: "map the hub's dependencies"},
		{Tool: contracts.ToolSearch, Args: map[string]any{"pattern": "Pipeline"}, Reason: "find every use of the hub type"},
		{Tool: contracts.ToolListDir, Args: map[string]any{"path": "web"}, Reason: "there is a second language in here"},
		{Tool: contracts.ToolReadFile, Args: map[string]any{"path": "web/index.js"}, Reason: "the dashboard entrypoint"},
		{Tool: contracts.ToolFollowImports, Args: map[string]any{"path": "web/index.js"}, Reason: "resolve the JS require graph"},
	}}
}

func (d *scriptedDecider) Decide(ctx context.Context, obs contracts.Observation) (contracts.Decision, error) {
	if err := ctx.Err(); err != nil {
		return contracts.Decision{}, err
	}
	d.calls++
	if d.calls <= len(d.script) {
		next := d.script[d.calls-1]
		if next.Tool == contracts.ToolReadFile {
			d.reads++
			// Keep the script honest: if the fixture changed, read whatever
			// the agent says is unread instead of a stale path.
			if path, ok := next.Args["path"].(string); ok && !contains(obs.Unread, path) && len(obs.Unread) > 0 {
				next.Args = map[string]any{"path": obs.Unread[0]}
			}
		}
		return next, nil
	}
	if len(obs.Unread) > 0 && d.reads < 6 {
		d.reads++
		return contracts.Decision{
			Tool:   contracts.ToolReadFile,
			Args:   map[string]any{"path": obs.Unread[0]},
			Reason: "keep reading the unread source files",
		}, nil
	}
	return contracts.Decision{Done: true, Reason: "enough of the repo is understood to write the report"}, nil
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// End-to-end: real tools + real loop + real report, stub brain.
// ---------------------------------------------------------------------------

func TestSmokeEndToEndWithStubModel(t *testing.T) {
	root := fixtureRoot(t)

	registry, err := tools.New(root)
	if err != nil {
		t.Fatalf("tools.New(%s): %v", root, err)
	}

	var trace bytes.Buffer
	brain := newScriptedDecider()
	explorer, err := agent.New(agent.Options{
		Root:     root,
		Registry: registry,
		Decider:  brain,
		Budget:   16,
		Trace:    &trace,
	})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := explorer.Run(ctx)
	if err != nil {
		t.Fatalf("agent.Run: %v", err)
	}
	if result == nil {
		t.Fatal("agent.Run returned a nil result")
	}

	if len(result.Visited) < 3 {
		t.Errorf("visited %d files (%v), want at least 3", len(result.Visited), result.Visited)
	}
	if edges := countEdges(result.Imports); edges < 1 {
		t.Errorf("resolved %d import edges, want at least 1 (raw: %v)", edges, result.RawImports)
	}
	if strings.TrimSpace(result.StoppedBecause) == "" {
		t.Error("StoppedBecause is empty; the agent must always say why it stopped")
	}
	if result.StepsUsed < 3 {
		t.Errorf("StepsUsed = %d, want at least 3", result.StepsUsed)
	}
	if len(result.Files) < 5 {
		t.Errorf("discovered %d source files in the fixture, want at least 5", len(result.Files))
	}
	if trace.Len() == 0 {
		t.Error("agent wrote nothing to the trace writer")
	}

	doc := report.Build(report.Input{
		RootPath:       result.RootPath,
		Files:          result.Files,
		Imports:        result.Imports,
		RawImports:     result.RawImports,
		Visited:        result.Visited,
		Notes:          result.Findings,
		Searches:       result.Searches,
		StepsUsed:      result.StepsUsed,
		Budget:         16,
		StoppedBecause: result.StoppedBecause,
		ModelName:      "stub-decider",
		Duration:       result.Duration,
		MaxNodes:       defaultMaxNodes,
		MaxStarters:    defaultStarters,
	})

	if len(doc.Starters) < 1 {
		t.Error(`report has no "read these first" entries`)
	}
	if strings.TrimSpace(doc.Diagram) == "" {
		t.Error("report has an empty Mermaid diagram")
	}

	md := report.Markdown(doc)
	if !strings.Contains(md, "```mermaid") {
		t.Error("markdown has no ```mermaid fence")
	}
	if !strings.Contains(strings.ToLower(md), "read these") {
		t.Error(`markdown has no "read these first" section`)
	}
	for _, starter := range doc.Starters {
		if !strings.Contains(md, starter.Path) {
			t.Errorf("starter %q is missing from the rendered markdown", starter.Path)
		}
	}
	if !strings.Contains(md, "core.py") && !strings.Contains(md, "main.py") {
		t.Errorf("markdown mentions no fixture file; got:\n%s", md)
	}

	// The report must never leak absolute host paths into the body.
	if strings.Count(md, root) > 1 {
		t.Errorf("markdown repeats the absolute root path %q in the body", root)
	}
}

func TestReportWriteToTempDir(t *testing.T) {
	root := fixtureRoot(t)

	files, err := tools.SourceFiles(root)
	if err != nil {
		t.Fatalf("tools.SourceFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("fixture %s has no source files", root)
	}

	doc := report.Build(report.Input{
		RootPath:       root,
		Files:          files,
		Imports:        map[string][]string{files[0].Path: {}},
		RawImports:     map[string][]string{},
		Visited:        []string{files[0].Path},
		StepsUsed:      1,
		Budget:         agent.DefaultBudget,
		StoppedBecause: "test",
		ModelName:      "stub-decider",
		MaxNodes:       defaultMaxNodes,
		MaxStarters:    defaultStarters,
	})

	path := filepath.Join(t.TempDir(), "nested", "CARTOGRAPHY.md")
	if err := report.Write(path, doc); err != nil {
		t.Fatalf("report.Write(%s): %v", path, err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back the report: %v", err)
	}
	if len(written) < 50 {
		t.Errorf("report is suspiciously short (%d bytes): %q", len(written), written)
	}
	if !strings.Contains(string(written), "```mermaid") {
		t.Error("written report has no ```mermaid fence")
	}
}

// ---------------------------------------------------------------------------
// Flags, environment and validation, through the run()/parseFlags() seams.
// ---------------------------------------------------------------------------

func noEnv(string) string { return "" }

func fakeEnv(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestParseFlagsDefaults(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseFlags(nil, &stderr, noEnv)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !filepath.IsAbs(cfg.repo) {
		t.Errorf("repo %q is not absolute", cfg.repo)
	}
	if cfg.model != model.DefaultModel {
		t.Errorf("model = %q, want %q", cfg.model, model.DefaultModel)
	}
	if cfg.ollamaURL != model.DefaultBaseURL {
		t.Errorf("ollamaURL = %q, want %q", cfg.ollamaURL, model.DefaultBaseURL)
	}
	if cfg.budget != agent.DefaultBudget {
		t.Errorf("budget = %d, want %d", cfg.budget, agent.DefaultBudget)
	}
	if cfg.out != defaultOut {
		t.Errorf("out = %q, want %q", cfg.out, defaultOut)
	}
	if cfg.starters != defaultStarters || cfg.maxNodes != defaultMaxNodes {
		t.Errorf("starters/maxNodes = %d/%d, want %d/%d", cfg.starters, cfg.maxNodes, defaultStarters, defaultMaxNodes)
	}
	if cfg.timeout != defaultTimeout || cfg.modelTimeout != defaultModelTimeout {
		t.Errorf("timeouts = %s/%s, want %s/%s", cfg.timeout, cfg.modelTimeout, defaultTimeout, defaultModelTimeout)
	}
	if cfg.verbose || cfg.skipHealth {
		t.Error("verbose/skip-health should default to false")
	}
}

func TestParseFlagsEnvFallbackAndPrecedence(t *testing.T) {
	root := fixtureRoot(t)
	env := fakeEnv(map[string]string{
		"CARTOGRAPHER_REPO":          root,
		"CARTOGRAPHER_MODEL":         "llama3.2:1b",
		"OLLAMA_HOST":                "10.0.0.5:1234",
		"CARTOGRAPHER_BUDGET":        "7",
		"CARTOGRAPHER_OUT":           "env.md",
		"CARTOGRAPHER_TIMEOUT":       "90s",
		"CARTOGRAPHER_MODEL_TIMEOUT": "30s",
		"CARTOGRAPHER_STARTERS":      "3",
		"CARTOGRAPHER_MAX_NODES":     "12",
		"CARTOGRAPHER_VERBOSE":       "true",
		"CARTOGRAPHER_SKIP_HEALTH":   "1",
	})

	var stderr bytes.Buffer
	cfg, err := parseFlags(nil, &stderr, env)
	if err != nil {
		t.Fatalf("parseFlags with env: %v", err)
	}
	if cfg.repo != root {
		t.Errorf("repo = %q, want %q", cfg.repo, root)
	}
	if cfg.model != "llama3.2:1b" {
		t.Errorf("model = %q, want the env value", cfg.model)
	}
	if cfg.ollamaURL != "http://10.0.0.5:1234" {
		t.Errorf("ollamaURL = %q, want http://10.0.0.5:1234", cfg.ollamaURL)
	}
	if cfg.budget != 7 || cfg.starters != 3 || cfg.maxNodes != 12 {
		t.Errorf("budget/starters/maxNodes = %d/%d/%d, want 7/3/12", cfg.budget, cfg.starters, cfg.maxNodes)
	}
	if cfg.out != "env.md" {
		t.Errorf("out = %q, want env.md", cfg.out)
	}
	if cfg.timeout != 90*time.Second || cfg.modelTimeout != 30*time.Second {
		t.Errorf("timeouts = %s/%s, want 1m30s/30s", cfg.timeout, cfg.modelTimeout)
	}
	if !cfg.verbose || !cfg.skipHealth {
		t.Error("verbose and skip-health should both be on from the environment")
	}

	// Flags must beat the environment.
	cfg, err = parseFlags([]string{"--model", "qwen2.5:7b", "--budget", "11", "--ollama-url", "http://localhost:9999/"}, &stderr, env)
	if err != nil {
		t.Fatalf("parseFlags with flags+env: %v", err)
	}
	if cfg.model != "qwen2.5:7b" || cfg.budget != 11 || cfg.ollamaURL != "http://localhost:9999" {
		t.Errorf("flags did not win: model=%q budget=%d url=%q", cfg.model, cfg.budget, cfg.ollamaURL)
	}
}

func TestParseFlagsIgnoresJunkEnvWithWarning(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := parseFlags(nil, &stderr, fakeEnv(map[string]string{
		"CARTOGRAPHER_BUDGET":  "lots",
		"CARTOGRAPHER_TIMEOUT": "soon",
	}))
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if cfg.budget != agent.DefaultBudget || cfg.timeout != defaultTimeout {
		t.Errorf("junk env was not ignored: budget=%d timeout=%s", cfg.budget, cfg.timeout)
	}
	if !strings.Contains(stderr.String(), "CARTOGRAPHER_BUDGET") {
		t.Errorf("no warning about the bad env var; stderr=%q", stderr.String())
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "definitely-not-here")
	file := filepath.Join(t.TempDir(), "a-file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}

	cases := []struct {
		name string
		args []string
	}{
		{"missing repo", []string{"--repo", missing}},
		{"repo is a file", []string{"--repo", file}},
		{"empty repo", []string{"--repo", "  "}},
		{"zero budget", []string{"--budget", "0"}},
		{"negative starters", []string{"--starters", "-1"}},
		{"zero max nodes", []string{"--max-nodes", "0"}},
		{"empty out", []string{"--out", ""}},
		{"bad url scheme", []string{"--ollama-url", "ftp://localhost:11434"}},
		{"unknown flag", []string{"--nope"}},
		{"stray argument", []string{"some-repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if _, err := parseFlags(tc.args, &stderr, noEnv); err == nil {
				t.Fatalf("parseFlags(%v) succeeded, want an error", tc.args)
			}
		})
	}
}

func TestNormaliseBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                           model.DefaultBaseURL,
		"localhost:11434":            "http://localhost:11434",
		"  127.0.0.1:11434  ":        "http://127.0.0.1:11434",
		"http://localhost:11434/":    "http://localhost:11434",
		"https://ollama.lan:443/api": "https://ollama.lan:443/api",
		"ollama-box":                 "http://ollama-box",
	}
	for in, want := range cases {
		got, err := normaliseBaseURL(in)
		if err != nil {
			t.Errorf("normaliseBaseURL(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normaliseBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"ftp://host", "http://", "://x"} {
		if got, err := normaliseBaseURL(bad); err == nil {
			t.Errorf("normaliseBaseURL(%q) = %q, want an error", bad, got)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here")

	t.Run("bad repo is a usage error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--repo", missing}, &stdout, &stderr); code != exitUsage {
			t.Errorf("exit code = %d, want %d", code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "does not exist") {
			t.Errorf("stderr should explain the bad path; got %q", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout must stay clean, got %q", stdout.String())
		}
	})

	t.Run("version exits ok", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--version"}, &stdout, &stderr); code != exitOK {
			t.Errorf("exit code = %d, want %d", code, exitOK)
		}
		if !strings.Contains(stderr.String(), version) {
			t.Errorf("stderr should carry the version; got %q", stderr.String())
		}
	})

	t.Run("help exits ok", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := run([]string{"--help"}, &stdout, &stderr); code != exitOK {
			t.Errorf("exit code = %d, want %d", code, exitOK)
		}
		if !strings.Contains(stderr.String(), "--repo") && !strings.Contains(stderr.String(), "-repo") {
			t.Errorf("help should list the flags; got %q", stderr.String())
		}
	})
}

// ---------------------------------------------------------------------------
// Standing proof of the local-only inference constraint.
// ---------------------------------------------------------------------------

// TestNoPaidAPIEndpoints walks every Go source file in the module (excluding
// testdata fixtures) and fails if any of them reference a hosted AI API or its
// credentials. The needles are assembled at runtime so this guard cannot flag
// itself.
func TestNoPaidAPIEndpoints(t *testing.T) {
	needles := []string{
		"api." + "anthropic" + ".com",
		"api." + "openai" + ".com",
		"clau" + "de-",
		"gpt" + "-4",
		"ANTHROPIC" + "_",
		"OPENAI" + "_",
	}

	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving module root: %v", err)
	}

	scanned := 0
	err = filepath.WalkDir(moduleRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			if name == "testdata" || name == ".git" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		lowered := strings.ToLower(string(data))
		rel, _ := filepath.Rel(moduleRoot, path)
		for _, needle := range needles {
			if strings.Contains(lowered, strings.ToLower(needle)) {
				t.Errorf("%s references a hosted AI API (%q); all inference must be local Ollama", rel, needle)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", moduleRoot, err)
	}
	if scanned == 0 {
		t.Fatalf("scanned no Go files under %s", moduleRoot)
	}
}

// TestModelPackageIsTheOnlyNetworkUser documents the other half of the same
// constraint: nothing outside internal/model reaches for net/http.
func TestModelPackageIsTheOnlyNetworkUser(t *testing.T) {
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving module root: %v", err)
	}
	allowed := filepath.Join(moduleRoot, "internal", "model")

	err = filepath.WalkDir(moduleRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "testdata" || entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		// Test files may spin up an httptest server anywhere; this guard is
		// about production code.
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") || strings.HasPrefix(path, allowed) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// main.go legitimately mentions net/url for --ollama-url; only the
		// HTTP client itself is off limits outside internal/model.
		if strings.Contains(string(data), `"net/http"`) {
			rel, _ := filepath.Rel(moduleRoot, path)
			t.Errorf("%s imports net/http; only internal/model may talk to the network", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", moduleRoot, err)
	}
}
