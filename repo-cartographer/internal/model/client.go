// Package model implements contracts.Decider on top of a LOCAL open model
// served by Ollama (default qwen2.5:1.5b).
//
// This package is the only place in Repo Cartographer that performs network
// I/O, and it only ever talks to the configured Ollama base URL (default
// http://localhost:11434). There are no calls to Anthropic, OpenAI or any
// other hosted/paid API, so the tool runs fully offline.
//
// The design assumptions are shaped by the target model being tiny:
//
//   - BuildPrompt renders a short, bounded, example-heavy prompt, because a
//     1.5B model on a laptop CPU is both slow and easily confused.
//   - ParseDecision is deliberately very tolerant: small models emit prose,
//     markdown fences, Python-ish literals, trailing commas and key aliases.
//   - Decide retries exactly once with a terser reminder, then degrades to
//     FallbackDecision with a nil error, so a live demo can never hard-crash
//     on a bad generation.
package model

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// Defaults for Options. They are exported because the CLI prints them in
// --help and uses them as flag defaults.
const (
	// DefaultBaseURL is the standard local Ollama endpoint.
	DefaultBaseURL = "http://localhost:11434"
	// DefaultModel is a small instruct model that fits comfortably on a
	// laptop and still follows a JSON-only instruction most of the time.
	DefaultModel = "qwen2.5:1.5b"

	// defaultTimeout is generous: CPU inference of a few hundred tokens can
	// take tens of seconds.
	defaultTimeout = 120 * time.Second

	// Token ceilings for the two attempts. A decision is a single small JSON
	// object, so these are plenty; the retry gets a little more room in case
	// the first answer was truncated mid-object.
	numPredictFirst = 256
	numPredictRetry = 384
)

// Options configures a Client. Every zero-valued field falls back to a
// sensible default, so `New(Options{})` is a working client.
type Options struct {
	// BaseURL is the Ollama server root, e.g. "http://localhost:11434".
	BaseURL string
	// Model is the Ollama model tag, e.g. "qwen2.5:1.5b".
	Model string
	// Timeout bounds a single generation request. Ignored when HTTPClient is
	// supplied (that client's own Timeout wins).
	Timeout time.Duration
	// Temperature is passed through to Ollama; 0 keeps output as
	// deterministic as the runtime allows.
	Temperature float64
	// HTTPClient is an optional injection point for tests (httptest servers).
	HTTPClient *http.Client
	// Logger, when non-nil, receives trace lines (prompt/response sizes,
	// retries, fallbacks). The package never writes to stdout/stderr on its
	// own, so leaving this nil keeps it silent.
	Logger *log.Logger
}

// Stats is a snapshot of what the Client has done so far. The CLI and the
// report layer use it to be honest about how much of the run was the model and
// how much was the safety net.
type Stats struct {
	// Calls is the number of HTTP generation requests issued (retries
	// included).
	Calls int
	// Retries is the number of times a second attempt was needed.
	Retries int
	// Fallbacks is the number of decisions served by FallbackDecision.
	Fallbacks int
	// ParseFailures is the number of model responses that could not be
	// turned into a usable Decision (unparseable, or an unknown/empty tool).
	ParseFailures int
}

// Client is a contracts.Decider backed by a local Ollama server.
type Client struct {
	baseURL     string
	model       string
	temperature float64
	httpClient  *http.Client
	logger      *log.Logger

	mu    sync.Mutex
	stats Stats
}

// Compile-time proof that Client is the agent's brain.
var _ contracts.Decider = (*Client)(nil)

// New returns a Client, filling in defaults for any zero-valued option.
func New(opts Options) *Client {
	// Trailing slashes would produce "//api/generate" paths.
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	mdl := opts.Model
	if mdl == "" {
		mdl = DefaultModel
	}
	hc := opts.HTTPClient
	if hc == nil {
		to := opts.Timeout
		if to <= 0 {
			to = defaultTimeout
		}
		hc = &http.Client{Timeout: to}
	}
	return &Client{
		baseURL:     base,
		model:       mdl,
		temperature: opts.Temperature,
		httpClient:  hc,
		logger:      opts.Logger,
	}
}

// BaseURL reports the endpoint this client talks to (useful for CLI messages).
func (c *Client) BaseURL() string { return c.baseURL }

// Model reports the configured Ollama model tag.
func (c *Client) Model() string { return c.model }

// Stats returns a copy of the counters, safe for concurrent use.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// bump applies fn to the counters under the mutex.
func (c *Client) bump(fn func(s *Stats)) {
	c.mu.Lock()
	fn(&c.stats)
	c.mu.Unlock()
}

// tracef writes a trace line only when a Logger was supplied.
func (c *Client) tracef(format string, args ...any) {
	if c.logger != nil {
		c.logger.Printf(format, args...)
	}
}

// Decide implements contracts.Decider.
//
// Flow: generate -> parse -> validate against the registry. On any failure
// (transport, non-200, unparseable output, or a tool name that is empty or
// unknown to the registry) it retries EXACTLY once with a blunter "JSON only"
// reminder. If that also fails it returns FallbackDecision(obs) and a NIL
// error: the agent loop keeps making progress instead of aborting the run.
func (c *Client) Decide(ctx context.Context, obs contracts.Observation) (contracts.Decision, error) {
	prompt := BuildPrompt(obs)

	if dec, ok := c.attempt(ctx, prompt, numPredictFirst, obs); ok {
		return dec, nil
	}

	// Second and final attempt: same state, much blunter instruction.
	c.bump(func(s *Stats) { s.Retries++ })
	c.tracef("model: retrying decision for step %d", obs.Step)

	if dec, ok := c.attempt(ctx, retryPrompt(prompt), numPredictRetry, obs); ok {
		return dec, nil
	}

	c.bump(func(s *Stats) { s.Fallbacks++ })
	fb := FallbackDecision(obs)
	c.tracef("model: fallback decision for step %d: %s %v", obs.Step, fb.Tool, fb.Args)
	return fb, nil
}

// attempt runs one generation and reports whether it yielded a *usable*
// decision: parseable, and either Done or naming a tool the registry actually
// advertises. An unknown tool name would make the agent loop dispatch
// something that does not exist, so it counts as a failure here.
func (c *Client) attempt(ctx context.Context, prompt string, numPredict int, obs contracts.Observation) (contracts.Decision, bool) {
	raw, err := c.generate(ctx, prompt, numPredict)
	if err != nil {
		c.tracef("model: generate failed: %v", err)
		return contracts.Decision{}, false
	}
	dec, err := ParseDecision(raw)
	if err != nil {
		c.bump(func(s *Stats) { s.ParseFailures++ })
		c.tracef("model: unparseable response (%d bytes): %v", len(raw), err)
		return contracts.Decision{}, false
	}
	if dec.Done {
		if dec.Args == nil {
			dec.Args = map[string]any{}
		}
		return dec, true
	}
	if !knownTool(dec.Tool, obs.Tools) {
		// Parsed fine, but there is nothing safe to dispatch.
		c.bump(func(s *Stats) { s.ParseFailures++ })
		c.tracef("model: unusable tool name %q", dec.Tool)
		return contracts.Decision{}, false
	}
	if dec.Args == nil {
		dec.Args = map[string]any{}
	}
	return dec, true
}

// knownTool reports whether name is advertised by the registry. When the
// observation carries no tool list (tests, early bootstrap) any non-empty name
// is accepted.
func knownTool(name string, tools []contracts.ToolInfo) bool {
	if name == "" {
		return false
	}
	if len(tools) == 0 {
		return true
	}
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}
