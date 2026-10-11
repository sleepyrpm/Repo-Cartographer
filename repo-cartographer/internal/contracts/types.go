// Package contracts holds the shared, FROZEN types that every other package in
// Repo Cartographer depends on. Nothing in here imports any other package of
// this module, so it can never create an import cycle.
//
// These definitions are the contract between the five modules:
//
//	internal/tools  implements Tool + Registry
//	internal/model  implements Decider (local Ollama model)
//	internal/agent  consumes Registry + Decider, produces Report data
//	internal/report renders a Report to markdown
//	cmd/cartographer wires them together
//
// Do not change the shape of anything here without updating all five.
package contracts

import "context"

// ---------------------------------------------------------------------------
// MCP-style tools
// ---------------------------------------------------------------------------

// Tool is one capability the agent can invoke, in the style of an MCP tool:
// a name, a human/model readable description and a JSON-ish argument map.
type Tool interface {
	// Name is the stable identifier the model uses to select this tool.
	Name() string
	// Description tells the model what the tool does and which args it takes.
	Description() string
	// Call runs the tool. Args are untyped (as they arrive from model JSON);
	// implementations must validate and coerce defensively.
	Call(args map[string]any) (ToolResult, error)
}

// ToolCall is a request to run a tool with arguments, as decided by the model.
type ToolCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// ToolResult is what a tool hands back. Text is the model-facing payload;
// Meta carries structured extras for the report layer (e.g. "imports",
// "entries", "lang") and may be nil.
type ToolResult struct {
	Text string         `json:"text"`
	Meta map[string]any `json:"meta,omitempty"`
}

// ToolInfo is the advertised shape of a tool, used when describing the
// available toolset to the model without handing it the implementations.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Registry is a lookup of all tools available to the agent.
type Registry interface {
	// Get returns the tool registered under name.
	Get(name string) (Tool, bool)
	// List returns every registered tool, in a stable order.
	List() []Tool
	// Describe returns the advertised name+description of every tool.
	Describe() []ToolInfo
}

// Canonical tool names. Every package refers to tools through these constants
// so a rename can never silently desynchronise the modules.
const (
	ToolReadFile      = "read_file"
	ToolListDir       = "list_dir"
	ToolSearch        = "search"
	ToolFollowImports = "follow_imports"
	ToolRepoTree      = "repo_tree"
)

// ---------------------------------------------------------------------------
// Repository facts
// ---------------------------------------------------------------------------

// RepoFile is one source file discovered in the repository.
// Path is always relative to the repo root and slash-separated.
type RepoFile struct {
	Path string `json:"path"`
	Lang string `json:"lang"`
	Size int    `json:"size"`
}

// ---------------------------------------------------------------------------
// The agent's brain
// ---------------------------------------------------------------------------

// Decision is the model's answer to "what next?": either a tool to call, or
// Done=true to stop. Reason is a short natural-language justification which is
// surfaced in the trace and the report.
type Decision struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	Done   bool           `json:"done"`
	Reason string         `json:"reason"`
}

// Exchange is one completed observe->act step: what was called and a
// (truncated) view of what came back. It is the agent's short-term memory.
type Exchange struct {
	Call   ToolCall `json:"call"`
	Result string   `json:"result"`
	Err    string   `json:"err,omitempty"`
}

// Observation is everything the model is told about the current state of the
// investigation before it makes the next Decision.
type Observation struct {
	RootPath   string     `json:"root_path"`
	Step       int        `json:"step"`   // 1-based index of the decision being made
	Budget     int        `json:"budget"` // total steps allowed
	Tools      []ToolInfo `json:"tools"`
	Tree       string     `json:"tree"`    // compact repo tree snapshot
	Visited    []string   `json:"visited"` // files already read
	Unread     []string   `json:"unread"`  // candidate source files not yet read
	Transcript []Exchange `json:"history"` // most recent steps first-to-last
}

// Decider is the agent's brain. internal/model implements it against a local
// Ollama model; tests implement it with deterministic stubs (no network).
type Decider interface {
	Decide(ctx context.Context, obs Observation) (Decision, error)
}

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

// Finding is a ranked "read this file first" recommendation with a one-line
// reason the reader can act on.
type Finding struct {
	Path string `json:"path"`
	Note string `json:"note"`
}

// Report is the finished artifact the tool prints to markdown.
type Report struct {
	RootPath string    `json:"root_path"`
	Diagram  string    `json:"diagram"`  // Mermaid source, without fences
	Starters []Finding `json:"starters"` // ranked, best first
	Summary  string    `json:"summary"`
	Visited  []string  `json:"visited"`
}
