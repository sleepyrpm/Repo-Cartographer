package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// assertLoopback fails the test unless raw points at the loopback interface.
// Every test server in this package must be local: the whole tool is required
// to work offline, and CI must never touch a real network address.
func assertLoopback(t *testing.T, raw string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("unparseable test URL %q: %v", raw, err)
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1", "[::1]":
	default:
		t.Fatalf("test would contact a non-loopback host %q", u.Hostname())
	}
}

// testTools is the registry view handed to the model in tests.
var testTools = []contracts.ToolInfo{
	{Name: contracts.ToolReadFile, Description: "Read a file by path."},
	{Name: contracts.ToolListDir, Description: "List a directory."},
	{Name: contracts.ToolSearch, Description: "Search the repo for a string."},
	{Name: contracts.ToolFollowImports, Description: "List the imports of a file."},
	{Name: contracts.ToolRepoTree, Description: "Snapshot the repo tree."},
}

func testObservation() contracts.Observation {
	return contracts.Observation{
		RootPath: "/repo",
		Step:     2,
		Budget:   10,
		Tools:    testTools,
		Tree:     "cmd/\n  main.go\ninternal/\n  app/app.go\n",
		Visited:  []string{"README.md"},
		Unread:   []string{"cmd/main.go", "internal/app/app.go"},
		Transcript: []contracts.Exchange{{
			Call:   contracts.ToolCall{Name: contracts.ToolReadFile, Args: map[string]any{"path": "README.md"}},
			Result: "# Project\nA thing that does stuff.",
		}},
	}
}

// scriptedServer replies with the given bodies in order (the last one repeats
// once exhausted) and records every request body it received.
type scriptedServer struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []map[string]any
	replies  []string
	status   []int
	idx      int
}

func newScriptedServer(t *testing.T, replies ...string) *scriptedServer {
	t.Helper()
	s := &scriptedServer{t: t, replies: replies}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	assertLoopback(t, s.srv.URL)
	return s
}

func (s *scriptedServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)

	s.mu.Lock()
	s.requests = append(s.requests, decoded)
	i := s.idx
	s.idx++
	s.mu.Unlock()

	if i >= len(s.replies) {
		i = len(s.replies) - 1
	}
	status := http.StatusOK
	if len(s.status) > 0 {
		j := i
		if j >= len(s.status) {
			j = len(s.status) - 1
		}
		status = s.status[j]
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Replies are the *model text*; wrap them the way Ollama does.
	payload, _ := json.Marshal(generateResponse{Response: s.replies[i], Done: true})
	if status != http.StatusOK {
		_, _ = io.WriteString(w, s.replies[i])
		return
	}
	_, _ = w.Write(payload)
}

func (s *scriptedServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *scriptedServer) request(i int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.requests) {
		s.t.Fatalf("no request #%d (only %d seen)", i, len(s.requests))
	}
	return s.requests[i]
}

func (s *scriptedServer) client(t *testing.T) *Client {
	t.Helper()
	return New(Options{BaseURL: s.srv.URL, Model: "qwen2.5:1.5b", HTTPClient: s.srv.Client()})
}

// ---------------------------------------------------------------------------
// defaults & plumbing
// ---------------------------------------------------------------------------

func TestNewAppliesDefaults(t *testing.T) {
	c := New(Options{})
	if c.BaseURL() != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL(), DefaultBaseURL)
	}
	if c.Model() != DefaultModel {
		t.Errorf("Model = %q, want %q", c.Model(), DefaultModel)
	}
	if c.httpClient == nil || c.httpClient.Timeout != defaultTimeout {
		t.Errorf("default timeout = %v, want %v", c.httpClient.Timeout, defaultTimeout)
	}
	// The default endpoint must be local: no hosted API, ever.
	assertLoopback(t, c.BaseURL())
	if !strings.HasPrefix(DefaultBaseURL, "http://localhost:") {
		t.Errorf("DefaultBaseURL %q is not a localhost endpoint", DefaultBaseURL)
	}
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	c := New(Options{BaseURL: "http://127.0.0.1:1234/"})
	if c.BaseURL() != "http://127.0.0.1:1234" {
		t.Errorf("BaseURL = %q, want trailing slash removed", c.BaseURL())
	}
}

// ---------------------------------------------------------------------------
// Decide
// ---------------------------------------------------------------------------

func TestDecideHappyPath(t *testing.T) {
	srv := newScriptedServer(t,
		`{"tool":"read_file","args":{"path":"cmd/main.go"},"done":false,"reason":"entrypoint"}`)
	c := srv.client(t)

	dec, err := c.Decide(context.Background(), testObservation())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Tool != contracts.ToolReadFile {
		t.Errorf("Tool = %q, want %q", dec.Tool, contracts.ToolReadFile)
	}
	if dec.Args["path"] != "cmd/main.go" {
		t.Errorf("Args = %#v", dec.Args)
	}
	if dec.Done {
		t.Error("Done should be false")
	}
	if srv.calls() != 1 {
		t.Errorf("made %d HTTP calls, want 1", srv.calls())
	}
	st := c.Stats()
	if st.Calls != 1 || st.Retries != 0 || st.Fallbacks != 0 || st.ParseFailures != 0 {
		t.Errorf("Stats = %+v, want 1 call and nothing else", st)
	}
}

func TestDecideRequestBodyShape(t *testing.T) {
	srv := newScriptedServer(t, `{"tool":"","args":{},"done":true,"reason":"clear"}`)
	c := New(Options{
		BaseURL:     srv.srv.URL,
		Model:       "my-model:latest",
		Temperature: 0.25,
		HTTPClient:  srv.srv.Client(),
	})
	if _, err := c.Decide(context.Background(), testObservation()); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	req := srv.request(0)
	if req["model"] != "my-model:latest" {
		t.Errorf("model = %#v", req["model"])
	}
	prompt, _ := req["prompt"].(string)
	if prompt == "" {
		t.Error("prompt must not be empty")
	}
	if !strings.Contains(prompt, contracts.ToolReadFile) {
		t.Error("prompt should advertise the registry tools")
	}
	if stream, ok := req["stream"].(bool); !ok || stream {
		t.Errorf("stream = %#v, want false", req["stream"])
	}
	if req["format"] != "json" {
		t.Errorf("format = %#v, want \"json\"", req["format"])
	}
	opts, ok := req["options"].(map[string]any)
	if !ok {
		t.Fatalf("options = %#v", req["options"])
	}
	if opts["temperature"] != 0.25 {
		t.Errorf("temperature = %#v, want 0.25", opts["temperature"])
	}
	if np, _ := opts["num_predict"].(float64); np != float64(numPredictFirst) {
		t.Errorf("num_predict = %#v, want %d", opts["num_predict"], numPredictFirst)
	}
}

func TestDecideRetriesOnceThenSucceeds(t *testing.T) {
	srv := newScriptedServer(t,
		"I think we should probably look at the main file, don't you?",
		`{"tool":"list_dir","args":{"path":"internal"},"done":false,"reason":"map packages"}`)
	c := srv.client(t)

	dec, err := c.Decide(context.Background(), testObservation())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Tool != contracts.ToolListDir {
		t.Errorf("Tool = %q, want %q", dec.Tool, contracts.ToolListDir)
	}
	if srv.calls() != 2 {
		t.Errorf("made %d HTTP calls, want exactly 2", srv.calls())
	}
	// The retry prompt must be blunter about JSON-only output.
	retry, _ := srv.request(1)["prompt"].(string)
	if !strings.Contains(retry, "ONLY") || !strings.Contains(retry, "INVALID") {
		t.Errorf("retry prompt is not the forceful variant: %q", truncate(retry, 120))
	}
	if np, _ := srv.request(1)["options"].(map[string]any)["num_predict"].(float64); np != float64(numPredictRetry) {
		t.Errorf("retry num_predict = %v, want %d", np, numPredictRetry)
	}
	st := c.Stats()
	if st.Calls != 2 || st.Retries != 1 || st.ParseFailures != 1 || st.Fallbacks != 0 {
		t.Errorf("Stats = %+v", st)
	}
}

func TestDecideRetriesThenFallsBack(t *testing.T) {
	srv := newScriptedServer(t, "no json here, sorry", "still no json")
	c := srv.client(t)
	obs := testObservation()

	dec, err := c.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide must not return an error when falling back, got %v", err)
	}
	if srv.calls() != 2 {
		t.Fatalf("made %d HTTP calls, want exactly 2", srv.calls())
	}
	if dec.Tool != contracts.ToolReadFile || dec.Args["path"] != obs.Unread[0] {
		t.Errorf("fallback decision = %#v, want read_file on %q", dec, obs.Unread[0])
	}
	if dec.Reason != FallbackReasonRead {
		t.Errorf("Reason = %q, want the explicit fallback reason %q", dec.Reason, FallbackReasonRead)
	}
	st := c.Stats()
	if st.Calls != 2 || st.Retries != 1 || st.Fallbacks != 1 || st.ParseFailures != 2 {
		t.Errorf("Stats = %+v", st)
	}
}

func TestDecideUnknownToolFallsBack(t *testing.T) {
	srv := newScriptedServer(t, `{"tool":"rm_rf","args":{},"done":false,"reason":"oops"}`)
	c := srv.client(t)
	obs := testObservation()

	dec, err := c.Decide(context.Background(), obs)
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if srv.calls() != 2 {
		t.Errorf("made %d HTTP calls, want 2 (one retry)", srv.calls())
	}
	if dec.Tool != contracts.ToolReadFile || dec.Reason != FallbackReasonRead {
		t.Errorf("decision = %#v, want the read_file fallback", dec)
	}
	if c.Stats().Fallbacks != 1 {
		t.Errorf("Stats = %+v, want one fallback", c.Stats())
	}
}

func TestDecideEmptyToolWithDoneFalseFallsBack(t *testing.T) {
	srv := newScriptedServer(t, `{"tool":"","args":{},"done":false,"reason":"unsure"}`)
	c := srv.client(t)

	dec, err := c.Decide(context.Background(), testObservation())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Reason != FallbackReasonRead {
		t.Errorf("decision = %#v, want fallback", dec)
	}
}

func TestDecideAcceptsDone(t *testing.T) {
	srv := newScriptedServer(t, `Thinking... {"tool":"","args":{},"done":true,"reason":"architecture is clear"}`)
	c := srv.client(t)

	dec, err := c.Decide(context.Background(), testObservation())
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.Done || dec.Tool != "" {
		t.Errorf("decision = %#v, want done with no tool", dec)
	}
	if dec.Args == nil {
		t.Error("Args must never be nil")
	}
	if srv.calls() != 1 {
		t.Errorf("made %d HTTP calls, want 1", srv.calls())
	}
}

func TestDecideNon200FallsBack(t *testing.T) {
	srv := newScriptedServer(t, "boom", "boom")
	srv.status = []int{http.StatusInternalServerError}
	c := srv.client(t)

	dec, err := c.Decide(context.Background(), testObservation())
	if err != nil {
		t.Fatalf("Decide must swallow transport failures, got %v", err)
	}
	if dec.Reason != FallbackReasonRead {
		t.Errorf("decision = %#v, want fallback", dec)
	}
	if srv.calls() != 2 {
		t.Errorf("made %d HTTP calls, want 2", srv.calls())
	}
}

// ---------------------------------------------------------------------------
// transport-level errors
// ---------------------------------------------------------------------------

func TestGenerateNon200Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":"model 'nope' not found"}`)
	}))
	defer srv.Close()
	assertLoopback(t, srv.URL)

	c := New(Options{BaseURL: srv.URL, Model: "nope", HTTPClient: srv.Client()})
	_, err := c.generate(context.Background(), "hi", 16)
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should include the status: %v", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should include a trimmed body: %v", err)
	}
}

func TestGenerateReportsOllamaError(t *testing.T) {
	srv := newScriptedServer(t, "unused")
	srv.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"error":"model requires more system memory"}`)
	})
	c := srv.client(t)
	if _, err := c.generate(context.Background(), "hi", 16); err == nil ||
		!strings.Contains(err.Error(), "more system memory") {
		t.Fatalf("err = %v, want the ollama error surfaced", err)
	}
}

func TestGenerateHonoursContextCancellation(t *testing.T) {
	// The handler never answers; release is closed before the server is shut
	// down so Close cannot block on the in-flight request.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	assertLoopback(t, srv.URL)

	c := New(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.generate(ctx, "hi", 16); err == nil {
		t.Fatal("expected a context error")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("generate did not abort promptly on context expiry")
	}
}

func TestDecideOnCancelledContextFallsBack(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	assertLoopback(t, srv.URL)

	c := New(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already dead

	obs := testObservation()
	obs.Unread = nil
	obs.Visited = nil
	dec, err := c.Decide(ctx, obs)
	if err != nil {
		t.Fatalf("Decide must not fail hard on a dead context, got %v", err)
	}
	if dec.Tool != contracts.ToolRepoTree || dec.Reason != FallbackReasonTree {
		t.Errorf("decision = %#v, want the repo_tree fallback", dec)
	}
	if c.Stats().Calls != 2 {
		t.Errorf("Stats = %+v, want 2 attempted calls", c.Stats())
	}
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func tagsServer(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("Health hit %q, want /api/tags", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("Health used %s, want GET", r.Method)
		}
		var body struct {
			Models []map[string]string `json:"models"`
		}
		for _, m := range models {
			body.Models = append(body.Models, map[string]string{"name": m, "model": m})
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	assertLoopback(t, srv.URL)
	return srv
}

func TestHealthModelPresent(t *testing.T) {
	srv := tagsServer(t, "llama3:8b", "qwen2.5:1.5b")
	c := New(Options{BaseURL: srv.URL, Model: "qwen2.5:1.5b", HTTPClient: srv.Client()})
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
}

func TestHealthModelPresentViaLatestSuffix(t *testing.T) {
	srv := tagsServer(t, "qwen2.5:latest")
	c := New(Options{BaseURL: srv.URL, Model: "qwen2.5", HTTPClient: srv.Client()})
	if err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health should treat %q as %q: %v", "qwen2.5", "qwen2.5:latest", err)
	}
}

func TestHealthModelMissing(t *testing.T) {
	srv := tagsServer(t, "llama3:8b")
	c := New(Options{BaseURL: srv.URL, Model: "qwen2.5:1.5b", HTTPClient: srv.Client()})
	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected an error when the model is not installed")
	}
	if !strings.Contains(err.Error(), "ollama pull qwen2.5:1.5b") {
		t.Errorf("error must name the exact fix command, got: %v", err)
	}
	if !strings.Contains(err.Error(), "llama3:8b") {
		t.Errorf("error should list what is installed, got: %v", err)
	}
}

func TestHealthServerDown(t *testing.T) {
	srv := tagsServer(t, "qwen2.5:1.5b")
	addr := srv.URL
	srv.Close() // nothing is listening now
	assertLoopback(t, addr)

	c := New(Options{BaseURL: addr, Model: "qwen2.5:1.5b", Timeout: 500 * time.Millisecond})
	err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected an error against a dead server")
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("error must tell the user to run `ollama serve`, got: %v", err)
	}
}

func TestHealthNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "proxy exploded")
	}))
	defer srv.Close()
	assertLoopback(t, srv.URL)

	c := New(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
	err := c.Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want the status surfaced", err)
	}
}

// TestClientSatisfiesDecider is belt-and-braces alongside the package-level
// compile-time assertion.
func TestClientSatisfiesDecider(t *testing.T) {
	var d contracts.Decider = New(Options{})
	if d == nil {
		t.Fatal("nil decider")
	}
}
