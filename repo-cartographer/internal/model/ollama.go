package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// generateRequest is the subset of Ollama's /api/generate payload we use.
//
// format:"json" nudges small models towards emitting valid JSON. It is a help,
// never a guarantee, so ParseDecision still assumes the worst.
type generateRequest struct {
	Model   string       `json:"model"`
	Prompt  string       `json:"prompt"`
	Stream  bool         `json:"stream"`
	Format  string       `json:"format"`
	Options generateOpts `json:"options"`
}

// generateOpts are the sampler knobs passed through to the runtime.
type generateOpts struct {
	Temperature float64 `json:"temperature"`
	NumPredict  int     `json:"num_predict"`
}

// generateResponse is the non-streaming reply shape. Ollama reports logical
// failures (e.g. "model not found") in the Error field with HTTP 200 or 404.
type generateResponse struct {
	Response string `json:"response"`
	Error    string `json:"error"`
	Done     bool   `json:"done"`
}

// maxErrBody bounds how much of a failing response body we quote in errors.
const maxErrBody = 300

// generate posts one prompt to {BaseURL}/api/generate and returns the raw
// model text. The caller's ctx is honoured, so a cancelled run aborts the
// in-flight HTTP request immediately.
func (c *Client) generate(ctx context.Context, prompt string, numPredict int) (string, error) {
	body, err := json.Marshal(generateRequest{
		Model:  c.model,
		Prompt: prompt,
		Stream: false,
		Format: "json",
		Options: generateOpts{
			Temperature: c.temperature,
			NumPredict:  numPredict,
		},
	})
	if err != nil {
		return "", fmt.Errorf("model: encoding request: %w", err)
	}

	url := c.baseURL + "/api/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("model: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	c.bump(func(s *Stats) { s.Calls++ })
	c.tracef("model: POST %s (%d byte prompt)", url, len(prompt))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("model: calling ollama at %s: %w (is `ollama serve` running?)", c.baseURL, err)
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("model: ollama %s returned %s: %s",
			url, resp.Status, trimForError(string(raw)))
	}
	if readErr != nil {
		return "", fmt.Errorf("model: reading ollama response: %w", readErr)
	}

	var out generateResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("model: decoding ollama response: %w (body: %s)", err, trimForError(string(raw)))
	}
	if out.Error != "" {
		return "", fmt.Errorf("model: ollama error: %s (try `ollama pull %s`)", out.Error, c.model)
	}
	if strings.TrimSpace(out.Response) == "" {
		return "", fmt.Errorf("model: ollama returned an empty response")
	}
	c.tracef("model: got %d byte response", len(out.Response))
	return out.Response, nil
}

// tagsResponse is the /api/tags listing of locally installed models.
type tagsResponse struct {
	Models []struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	} `json:"models"`
}

// Health pings the Ollama server and checks that the configured model is
// installed, so the CLI can print an actionable message up front instead of
// hanging for two minutes on the first decision.
//
// Every error it returns names the exact shell command that fixes it.
func (c *Client) Health(ctx context.Context) error {
	url := c.baseURL + "/api/tags"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("model: building health request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("model: cannot reach ollama at %s: %w\n"+
			"       start it with `ollama serve` (and `ollama pull %s` if you have not yet)",
			c.baseURL, err, c.model)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model: ollama at %s returned %s: %s\n"+
			"       is `ollama serve` healthy?", c.baseURL, resp.Status, trimForError(string(raw)))
	}

	var tags tagsResponse
	if err := json.Unmarshal(raw, &tags); err != nil {
		return fmt.Errorf("model: cannot decode %s: %w (body: %s)", url, err, trimForError(string(raw)))
	}

	installed := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		for _, n := range []string{m.Name, m.Model} {
			if n == "" {
				continue
			}
			installed = append(installed, n)
			if modelMatches(n, c.model) {
				c.tracef("model: health ok, %s present at %s", c.model, c.baseURL)
				return nil
			}
		}
	}

	return fmt.Errorf("model: ollama at %s is up but model %q is not installed (have: %s)\n"+
		"       fix it with `ollama pull %s`",
		c.baseURL, c.model, summariseList(installed), c.model)
}

// modelMatches compares an installed tag against the configured one, allowing
// for Ollama's implicit ":latest" suffix in both directions.
func modelMatches(installed, want string) bool {
	installed = strings.TrimSpace(installed)
	want = strings.TrimSpace(want)
	if installed == "" || want == "" {
		return false
	}
	if strings.EqualFold(installed, want) {
		return true
	}
	norm := func(s string) string {
		if !strings.Contains(s, ":") {
			return strings.ToLower(s) + ":latest"
		}
		return strings.ToLower(s)
	}
	return norm(installed) == norm(want)
}

// summariseList renders a short, deduplicated "a, b, c" for error messages.
func summariseList(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	seen := make(map[string]bool, len(items))
	uniq := make([]string, 0, len(items))
	for _, it := range items {
		if seen[it] {
			continue
		}
		seen[it] = true
		uniq = append(uniq, it)
		if len(uniq) == 8 {
			uniq = append(uniq, "...")
			break
		}
	}
	return strings.Join(uniq, ", ")
}

// trimForError collapses whitespace and clips a body for inclusion in errors.
func trimForError(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxErrBody {
		return s[:maxErrBody] + "..."
	}
	if s == "" {
		return "<empty body>"
	}
	return s
}
