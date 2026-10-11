package model

import (
	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// Reasons attached to fallback decisions. They are deliberately prefixed with
// "fallback:" and worded for a human, because they are surfaced verbatim in
// the agent trace and the final report: a run that leaned on the safety net
// should look like one, not like a confident model.
const (
	// FallbackReasonRead is used when there is still unread source to open.
	FallbackReasonRead = "fallback: model output unparseable, opening next unread source file"
	// FallbackReasonTree is used at the very start of a run, before anything
	// has been read, when a tree snapshot is the cheapest way to make progress.
	FallbackReasonTree = "fallback: model output unparseable, taking a repo tree snapshot"
	// FallbackReasonDone ends the run when there is genuinely nothing left.
	FallbackReasonDone = "fallback: nothing left to read"
)

// FallbackDecision is the safe default used when the model cannot produce a
// usable decision even after a retry. It never fails and never picks a tool
// that would be a no-op:
//
//  1. unread candidates remain -> read the first one;
//  2. nothing has been visited yet -> take a repo tree snapshot;
//  3. otherwise -> stop.
//
// Decide returns this with a nil error so a demo can never hard-crash on a
// bad generation.
func FallbackDecision(obs contracts.Observation) contracts.Decision {
	if len(obs.Unread) > 0 {
		return contracts.Decision{
			Tool:   contracts.ToolReadFile,
			Args:   map[string]any{"path": obs.Unread[0]},
			Done:   false,
			Reason: FallbackReasonRead,
		}
	}
	if len(obs.Visited) == 0 {
		return contracts.Decision{
			Tool:   contracts.ToolRepoTree,
			Args:   map[string]any{},
			Done:   false,
			Reason: FallbackReasonTree,
		}
	}
	return contracts.Decision{
		Tool:   "",
		Args:   map[string]any{},
		Done:   true,
		Reason: FallbackReasonDone,
	}
}
