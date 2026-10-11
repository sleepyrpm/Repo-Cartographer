// Package agent implements the heart of Repo Cartographer: an autonomous
// observe -> decide -> act -> record loop.
//
// The agent is handed two collaborators and nothing else:
//
//	a contracts.Registry  - the MCP-style toolbox it can act with
//	a contracts.Decider   - the brain (a local Ollama model, or a test stub)
//
// On every iteration the agent
//
//  1. OBSERVES - packs the current state of the investigation (repo tree,
//     files read so far, a ranked shortlist of promising unread files and a
//     bounded transcript of recent tool exchanges) into a contracts.Observation.
//  2. DECIDES  - asks the Decider for exactly one contracts.Decision: one tool
//     call, or Done.
//  3. ACTS     - dispatches that tool call through the registry.
//  4. RECORDS  - folds the structured ToolResult.Meta back into its knowledge
//     of the repository (visited files, file inventory, import edges, search
//     patterns, findings) and appends the exchange to the trace.
//
// Everything in this package is defensive on purpose: a missing tool, a
// hallucinated tool name, a Decider error, a tool error, a panicking tool, a
// nil Meta map, a Meta value that arrived as []any after a JSON round trip, a
// model that loops on the same file forever - none of these abort the run. The
// agent substitutes a safe default, records what happened and keeps going, so a
// live demo can never crash and always produces a usable Result.
//
// This package depends only on internal/contracts and the standard library.
package agent
