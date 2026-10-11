// Command cartographer maps a local repository: an autonomous observe -> decide
// -> act loop, driven by a small open-weights model served by a local Ollama,
// explores the repo with MCP-style tools and emits a markdown report containing
// a Mermaid architecture diagram and a ranked "read these files first" guide.
//
// All inference is local. The only network traffic is HTTP to the configured
// Ollama base URL (default http://localhost:11434); no hosted or paid API is
// ever contacted.
//
// Human-readable output (progress, the --verbose trace, errors, hints) goes to
// stderr. stdout is reserved for the report itself, and only when --out is "-".
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/agent"
	"github.com/ayush-2007-iit/repo-cartographer/internal/model"
	"github.com/ayush-2007-iit/repo-cartographer/internal/report"
	"github.com/ayush-2007-iit/repo-cartographer/internal/tools"
)

// version is the build stamp printed by --version.
const version = "0.1.0"

// Process exit codes.
const (
	exitOK      = 0 // report written
	exitFailure = 1 // runtime failure (tools, model, agent, I/O)
	exitUsage   = 2 // bad flags, or --repo is not a usable directory
)

// Defaults that are not owned by another package.
const (
	defaultOut          = "CARTOGRAPHY.md"
	defaultTimeout      = 10 * time.Minute
	defaultModelTimeout = 120 * time.Second
	defaultStarters     = report.DefaultMaxStarters
	defaultMaxNodes     = 40

	// healthTimeout bounds the pre-flight Ollama probe so a wedged server
	// does not eat the whole run budget.
	healthTimeout = 15 * time.Second
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// config is the fully resolved, validated configuration of one run.
type config struct {
	repo         string // absolute, verified directory
	model        string
	ollamaURL    string // normalised, e.g. "http://localhost:11434"
	budget       int
	out          string // "-" means stdout
	timeout      time.Duration
	modelTimeout time.Duration
	starters     int
	maxNodes     int
	verbose      bool
	skipHealth   bool
	showVersion  bool
}

// run is the testable entrypoint: it parses args, does the work and returns the
// process exit code. It never calls os.Exit itself.
func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseFlags(args, stderr, os.Getenv)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		fmt.Fprintf(stderr, "cartographer: %v\n", err)
		fmt.Fprintf(stderr, "try 'cartographer --help' for usage\n")
		return exitUsage
	}
	if cfg.showVersion {
		fmt.Fprintf(stderr, "cartographer %s\n", version)
		return exitOK
	}
	if err := cartograph(cfg, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "cartographer: %v\n", err)
		return exitFailure
	}
	return exitOK
}

// ---------------------------------------------------------------------------
// Flags
// ---------------------------------------------------------------------------

// parseFlags resolves every option from (in order of increasing precedence)
// the built-in default, the environment, and the command line. getenv is a seam
// so tests can supply an environment without mutating the process.
func parseFlags(args []string, stderr io.Writer, getenv func(string) string) (*config, error) {
	fs := flag.NewFlagSet("cartographer", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr, fs) }

	var (
		repo         = fs.String("repo", envString(getenv, ".", "CARTOGRAPHER_REPO"), "path to the repository to map")
		modelName    = fs.String("model", envString(getenv, model.DefaultModel, "CARTOGRAPHER_MODEL"), "local Ollama model to think with")
		ollamaURL    = fs.String("ollama-url", envString(getenv, model.DefaultBaseURL, "CARTOGRAPHER_OLLAMA_URL", "OLLAMA_HOST"), "Ollama base URL or host:port")
		budget       = fs.Int("budget", envInt(getenv, stderr, agent.DefaultBudget, "CARTOGRAPHER_BUDGET"), "maximum exploration steps")
		out          = fs.String("out", envString(getenv, defaultOut, "CARTOGRAPHER_OUT"), `report output path ("-" for stdout)`)
		timeout      = fs.Duration("timeout", envDuration(getenv, stderr, defaultTimeout, "CARTOGRAPHER_TIMEOUT"), "overall run timeout (0 for none)")
		modelTimeout = fs.Duration("model-timeout", envDuration(getenv, stderr, defaultModelTimeout, "CARTOGRAPHER_MODEL_TIMEOUT"), "per-request model timeout")
		starters     = fs.Int("starters", envInt(getenv, stderr, defaultStarters, "CARTOGRAPHER_STARTERS"), `how many files to rank in "read these first"`)
		maxNodes     = fs.Int("max-nodes", envInt(getenv, stderr, defaultMaxNodes, "CARTOGRAPHER_MAX_NODES"), "maximum nodes in the Mermaid diagram")
		verbose      = fs.Bool("verbose", envBool(getenv, stderr, false, "CARTOGRAPHER_VERBOSE"), "stream the per-step agent trace to stderr")
		verboseShort = fs.Bool("v", false, "shorthand for --verbose")
		skipHealth   = fs.Bool("skip-health", envBool(getenv, stderr, false, "CARTOGRAPHER_SKIP_HEALTH"), "skip the pre-flight Ollama health check")
		showVersion  = fs.Bool("version", false, "print the version and exit")
	)

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return nil, fmt.Errorf("unexpected argument %q (did you mean --repo %s?)", rest[0], rest[0])
	}

	cfg := &config{
		model:        strings.TrimSpace(*modelName),
		budget:       *budget,
		out:          strings.TrimSpace(*out),
		timeout:      *timeout,
		modelTimeout: *modelTimeout,
		starters:     *starters,
		maxNodes:     *maxNodes,
		verbose:      *verbose || *verboseShort,
		skipHealth:   *skipHealth,
		showVersion:  *showVersion,
	}
	if cfg.showVersion {
		return cfg, nil
	}

	if cfg.model == "" {
		return nil, errors.New("--model must not be empty")
	}
	if cfg.out == "" {
		return nil, errors.New(`--out must not be empty (use "-" to write to stdout)`)
	}
	if cfg.budget < 1 {
		return nil, fmt.Errorf("--budget must be at least 1, got %d", cfg.budget)
	}
	if cfg.starters < 1 {
		return nil, fmt.Errorf("--starters must be at least 1, got %d", cfg.starters)
	}
	if cfg.maxNodes < 1 {
		return nil, fmt.Errorf("--max-nodes must be at least 1, got %d", cfg.maxNodes)
	}
	if cfg.modelTimeout <= 0 {
		return nil, fmt.Errorf("--model-timeout must be positive, got %s", cfg.modelTimeout)
	}
	if cfg.timeout < 0 {
		return nil, fmt.Errorf("--timeout must not be negative, got %s", cfg.timeout)
	}

	normalised, err := normaliseBaseURL(*ollamaURL)
	if err != nil {
		return nil, fmt.Errorf("--ollama-url: %w", err)
	}
	cfg.ollamaURL = normalised

	root, err := resolveRepo(*repo)
	if err != nil {
		return nil, err
	}
	cfg.repo = root

	return cfg, nil
}

// usage prints the help text: a synopsis, then the flag defaults.
func usage(w io.Writer, fs *flag.FlagSet) {
	fmt.Fprintf(w, "cartographer %s - agentic repo explorer (local model via Ollama)\n\n", version)
	fmt.Fprintf(w, "usage: cartographer [flags]\n\n")
	fmt.Fprintf(w, "example:\n  cartographer --repo ../some-project --out CARTOGRAPHY.md\n\n")
	fmt.Fprintf(w, "flags:\n")
	fs.PrintDefaults()
	fmt.Fprintf(w, "\nevery flag also has an environment fallback (CARTOGRAPHER_*); the flag wins.\n")
}

// resolveRepo turns a user-supplied path into an absolute directory, failing
// with an actionable usage error when it is missing or not a directory.
func resolveRepo(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("--repo must not be empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("--repo %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("--repo %q does not exist", abs)
		}
		return "", fmt.Errorf("--repo %q: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("--repo %q is not a directory", abs)
	}
	return abs, nil
}

// normaliseBaseURL accepts a full URL ("http://localhost:11434"), a bare
// host:port ("localhost:11434") or a bare host, and returns a clean base URL
// with no trailing slash.
func normaliseBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return model.DefaultBaseURL, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a valid URL: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("unsupported scheme %q (want http or https)", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%q has no host", raw)
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.Path, "/"), nil
}

// envString returns the first non-empty environment value among keys, else def.
func envString(getenv func(string) string, def string, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
	}
	return def
}

// envInt reads an integer from the environment, warning and falling back to def
// when the value is unparseable.
func envInt(getenv func(string) string, stderr io.Writer, def int, keys ...string) int {
	raw := envString(getenv, "", keys...)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		fmt.Fprintf(stderr, "cartographer: ignoring %s=%q: not an integer\n", keys[0], raw)
		return def
	}
	return n
}

// envDuration reads a Go duration ("90s", "5m") from the environment.
func envDuration(getenv func(string) string, stderr io.Writer, def time.Duration, keys ...string) time.Duration {
	raw := envString(getenv, "", keys...)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		fmt.Fprintf(stderr, "cartographer: ignoring %s=%q: not a duration (try 90s or 5m)\n", keys[0], raw)
		return def
	}
	return d
}

// envBool reads a boolean ("1", "true", "no", ...) from the environment.
func envBool(getenv func(string) string, stderr io.Writer, def bool, keys ...string) bool {
	raw := envString(getenv, "", keys...)
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(strings.ToLower(raw))
	if err != nil {
		fmt.Fprintf(stderr, "cartographer: ignoring %s=%q: not a boolean\n", keys[0], raw)
		return def
	}
	return b
}

// ---------------------------------------------------------------------------
// The run itself
// ---------------------------------------------------------------------------

// cartograph wires the five packages together: tools -> model -> agent ->
// report. It is interruptible: on SIGINT/SIGTERM (or --timeout) the agent
// returns a partial result, which is still written out.
func cartograph(cfg *config, stdout, stderr io.Writer) error {
	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	if cfg.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.timeout)
		defer cancel()
	}

	registry, err := tools.New(cfg.repo)
	if err != nil {
		return fmt.Errorf("preparing tools for %s: %w", cfg.repo, err)
	}

	client := model.New(model.Options{
		BaseURL: cfg.ollamaURL,
		Model:   cfg.model,
		Timeout: cfg.modelTimeout,
	})

	if !cfg.skipHealth {
		if err := checkModel(ctx, client, cfg, stderr); err != nil {
			return err
		}
	}

	var trace io.Writer
	if cfg.verbose {
		trace = stderr
	}
	explorer, err := agent.New(agent.Options{
		Root:     cfg.repo,
		Registry: registry,
		Decider:  client,
		Budget:   cfg.budget,
		Trace:    trace,
	})
	if err != nil {
		return fmt.Errorf("starting agent: %w", err)
	}

	fmt.Fprintf(stderr, "mapping %s with %s (budget %d steps)\n", cfg.repo, cfg.model, cfg.budget)

	result, runErr := explorer.Run(ctx)
	if result == nil {
		if runErr == nil {
			runErr = errors.New("agent returned no result")
		}
		return fmt.Errorf("exploring %s: %w", cfg.repo, runErr)
	}
	if runErr != nil {
		// A partial result is still worth reporting (Ctrl-C, timeout, a
		// model that went away mid-run).
		fmt.Fprintf(stderr, "warning: exploration stopped early: %v\n", runErr)
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
		Budget:         cfg.budget,
		StoppedBecause: result.StoppedBecause,
		ModelName:      cfg.model,
		Duration:       result.Duration,
		MaxNodes:       cfg.maxNodes,
		MaxStarters:    cfg.starters,
	})

	destination := cfg.out
	if cfg.out == "-" {
		if _, err := io.WriteString(stdout, report.Markdown(doc)); err != nil {
			return fmt.Errorf("writing report to stdout: %w", err)
		}
		destination = "stdout"
	} else {
		if err := report.Write(cfg.out, doc); err != nil {
			return fmt.Errorf("writing report: %w", err)
		}
	}

	printSummary(stderr, cfg, result, client.Stats(), destination)
	return nil
}

// checkModel probes the local Ollama server before the loop starts, so the user
// gets one actionable message instead of 25 failed generations.
func checkModel(ctx context.Context, client *model.Client, cfg *config, stderr io.Writer) error {
	probe := healthTimeout
	if cfg.modelTimeout < probe {
		probe = cfg.modelTimeout
	}
	hctx, cancel := context.WithTimeout(ctx, probe)
	defer cancel()

	if err := client.Health(hctx); err != nil {
		fmt.Fprintf(stderr, "cartographer: cannot reach the local model: %v\n\n", err)
		fmt.Fprintf(stderr, "  1. start the server:  ollama serve\n")
		fmt.Fprintf(stderr, "  2. pull the model:    ollama pull %s\n", cfg.model)
		fmt.Fprintf(stderr, "  3. check the address: --ollama-url %s\n\n", cfg.ollamaURL)
		fmt.Fprintf(stderr, "pass --skip-health to try anyway.\n")
		return fmt.Errorf("model health check failed: %w", err)
	}
	return nil
}

// printSummary reports what the run actually did, on stderr.
func printSummary(stderr io.Writer, cfg *config, result *agent.Result, stats model.Stats, destination string) {
	fmt.Fprintf(stderr, "\n")
	fmt.Fprintf(stderr, "steps used:      %d/%d\n", result.StepsUsed, cfg.budget)
	fmt.Fprintf(stderr, "files read:      %d of %d source files\n", len(result.Visited), len(result.Files))
	fmt.Fprintf(stderr, "import edges:    %d\n", countEdges(result.Imports))
	fmt.Fprintf(stderr, "model calls:     %d (retries %d, fallbacks %d, parse failures %d)\n",
		stats.Calls, stats.Retries, stats.Fallbacks, stats.ParseFailures)
	fmt.Fprintf(stderr, "stopped because: %s\n", result.StoppedBecause)
	fmt.Fprintf(stderr, "elapsed:         %s\n", result.Duration.Round(time.Millisecond))
	fmt.Fprintf(stderr, "report:          %s\n", destination)
}

// countEdges totals the resolved intra-repo import edges.
func countEdges(imports map[string][]string) int {
	n := 0
	for _, targets := range imports {
		n += len(targets)
	}
	return n
}
