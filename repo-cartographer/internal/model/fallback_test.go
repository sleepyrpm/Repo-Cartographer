package model

import (
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

func TestFallbackDecisionReadsFirstUnread(t *testing.T) {
	obs := contracts.Observation{
		Visited: []string{"README.md"},
		Unread:  []string{"cmd/main.go", "internal/app/app.go"},
	}
	dec := FallbackDecision(obs)
	if dec.Tool != contracts.ToolReadFile {
		t.Errorf("Tool = %q, want %q", dec.Tool, contracts.ToolReadFile)
	}
	if dec.Args["path"] != "cmd/main.go" {
		t.Errorf("Args = %#v, want the first unread path", dec.Args)
	}
	if dec.Done {
		t.Error("Done must be false while files remain unread")
	}
	if dec.Reason != FallbackReasonRead {
		t.Errorf("Reason = %q, want %q", dec.Reason, FallbackReasonRead)
	}
}

func TestFallbackDecisionTreeWhenNothingVisited(t *testing.T) {
	dec := FallbackDecision(contracts.Observation{})
	if dec.Tool != contracts.ToolRepoTree {
		t.Errorf("Tool = %q, want %q", dec.Tool, contracts.ToolRepoTree)
	}
	if dec.Done {
		t.Error("Done must be false on the very first step")
	}
	if dec.Reason != FallbackReasonTree {
		t.Errorf("Reason = %q, want %q", dec.Reason, FallbackReasonTree)
	}
	if dec.Args == nil {
		t.Error("Args must never be nil")
	}
}

func TestFallbackDecisionDoneWhenExhausted(t *testing.T) {
	dec := FallbackDecision(contracts.Observation{Visited: []string{"a.go"}})
	if !dec.Done {
		t.Error("Done must be true when there is nothing left to read")
	}
	if dec.Tool != "" {
		t.Errorf("Tool = %q, want empty", dec.Tool)
	}
	if dec.Reason != FallbackReasonDone {
		t.Errorf("Reason = %q, want %q", dec.Reason, FallbackReasonDone)
	}
}

// Every fallback reason must be self-identifying: the report and trace print
// them verbatim, and a run that used the safety net should look like one.
func TestFallbackReasonsAreLabelled(t *testing.T) {
	for _, r := range []string{FallbackReasonRead, FallbackReasonTree, FallbackReasonDone} {
		if len(r) < 10 || r[:9] != "fallback:" {
			t.Errorf("reason %q should start with \"fallback:\"", r)
		}
	}
}
