package model

import (
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// TestParseDecision is the core robustness table: every row is output a tiny
// model has plausibly produced. want* fields of "" / nil mean "not asserted".
func TestParseDecision(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantTool   string
		wantDone   bool
		wantArgs   map[string]any
		wantReason string
	}{
		{
			name:       "clean json",
			raw:        `{"tool":"read_file","args":{"path":"main.go"},"done":false,"reason":"entrypoint"}`,
			wantTool:   contracts.ToolReadFile,
			wantArgs:   map[string]any{"path": "main.go"},
			wantReason: "entrypoint",
		},
		{
			name: "surrounded by prose and a greeting",
			raw: "Sure! Happy to help. Here is my next step:\n" +
				`{"tool":"list_dir","args":{"path":"."},"done":false,"reason":"see the layout"}` +
				"\nLet me know if you want something else!",
			wantTool: contracts.ToolListDir,
			wantArgs: map[string]any{"path": "."},
		},
		{
			name:     "markdown json fence",
			raw:      "```json\n{\"tool\":\"repo_tree\",\"args\":{},\"done\":false,\"reason\":\"orient\"}\n```",
			wantTool: contracts.ToolRepoTree,
			wantArgs: map[string]any{},
		},
		{
			name:     "bare fence",
			raw:      "```\n{\"tool\":\"search\",\"args\":{\"query\":\"func main\"},\"done\":false,\"reason\":\"find entry\"}\n```",
			wantTool: contracts.ToolSearch,
			wantArgs: map[string]any{"query": "func main"},
		},
		{
			name:     "brace inside a quoted string",
			raw:      `{"tool":"search","args":{"query":"func main() {"},"done":false,"reason":"brace"}`,
			wantTool: contracts.ToolSearch,
			wantArgs: map[string]any{"query": "func main() {"},
		},
		{
			name:     "escaped quote inside a string",
			raw:      `{"tool":"read_file","args":{"path":"a\"b.go"},"done":false,"reason":"odd name"}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": `a"b.go`},
		},
		{
			name:     "first balanced object is garbage, second is good",
			raw:      `{not json at all} then: {"tool":"repo_tree","args":{},"done":false,"reason":"ok"}`,
			wantTool: contracts.ToolRepoTree,
		},
		{
			name:     "trailing commas",
			raw:      `{"tool":"read_file","args":{"path":"a.go",},"done":false,"reason":"x",}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "a.go"},
		},
		{
			name:       "single quoted python style",
			raw:        `{'tool': 'read_file', 'args': {'path': 'main.go'}, 'done': False, 'reason': 'go file'}`,
			wantTool:   contracts.ToolReadFile,
			wantArgs:   map[string]any{"path": "main.go"},
			wantReason: "go file",
		},
		{
			name:     "python True and None",
			raw:      `{"tool": "search", "args": None, "done": True, "reason": "enough"}`,
			wantTool: contracts.ToolSearch,
			wantDone: true,
			wantArgs: map[string]any{},
		},
		{
			name:     "smart quotes",
			raw:      `{“tool”: “read_file”, “args”: {“path”: “x.go”}, “done”: false, “reason”: “smart”}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "x.go"},
		},
		{
			name:       "openai style name/arguments aliases",
			raw:        `{"name":"read_file","arguments":{"path":"a.go"},"finished":false,"thought":"look inside"}`,
			wantTool:   contracts.ToolReadFile,
			wantArgs:   map[string]any{"path": "a.go"},
			wantReason: "look inside",
		},
		{
			name:       "action/parameters/stop/why aliases",
			raw:        `{"action":"list_dir","parameters":{"path":"internal"},"stop":false,"why":"explore"}`,
			wantTool:   contracts.ToolListDir,
			wantArgs:   map[string]any{"path": "internal"},
			wantReason: "explore",
		},
		{
			name:       "tool_name/input/complete/explanation aliases",
			raw:        `{"tool_name":"follow_imports","input":{"path":"a.go"},"complete":false,"explanation":"deps"}`,
			wantTool:   contracts.ToolFollowImports,
			wantArgs:   map[string]any{"path": "a.go"},
			wantReason: "deps",
		},
		{
			name:     "nested tool_call wrapper",
			raw:      `{"tool_call":{"name":"follow_imports","args":{"path":"a.go"},"done":false,"reason":"deps"}}`,
			wantTool: contracts.ToolFollowImports,
			wantArgs: map[string]any{"path": "a.go"},
		},
		{
			name:     "nested action object wrapper",
			raw:      `{"action":{"name":"read_file","args":{"path":"go.mod"}},"done":false,"reason":"module"}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "go.mod"},
		},
		{
			name:     "function object with arguments",
			raw:      `{"function":{"name":"search","arguments":{"query":"http"}},"done":false,"reason":"net"}`,
			wantTool: contracts.ToolSearch,
			wantArgs: map[string]any{"query": "http"},
		},
		{
			name:     "array containing one object",
			raw:      `[{"tool":"repo_tree","args":{},"done":false,"reason":"start"}]`,
			wantTool: contracts.ToolRepoTree,
		},
		{
			name:     "done as string true",
			raw:      `{"tool":"","args":{},"done":"true","reason":"architecture clear"}`,
			wantTool: "",
			wantDone: true,
		},
		{
			name:     "done as yes",
			raw:      `{"tool":"","args":{},"done":"yes","reason":"clear"}`,
			wantDone: true,
		},
		{
			name:     "done as no",
			raw:      `{"tool":"read_file","args":{"path":"a.go"},"done":"no","reason":"more"}`,
			wantTool: contracts.ToolReadFile,
			wantDone: false,
		},
		{
			name:     "done as number",
			raw:      `{"tool":"","args":{},"done":1,"reason":"clear"}`,
			wantDone: true,
		},
		{
			name:     "args as a json string",
			raw:      `{"tool":"read_file","args":"{\"path\":\"cmd/main.go\"}","done":false,"reason":"entry"}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "cmd/main.go"},
		},
		{
			name:     "args as a bare path string",
			raw:      `{"tool":"read_file","args":"cmd/main.go","done":false,"reason":"entry"}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "cmd/main.go"},
		},
		{
			name:     "args null",
			raw:      `{"tool":"repo_tree","args":null,"done":false,"reason":"orient"}`,
			wantTool: contracts.ToolRepoTree,
			wantArgs: map[string]any{},
		},
		{
			name:     "args as single element array",
			raw:      `{"tool":"read_file","args":["main.go"],"done":false,"reason":"entry"}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "main.go"},
		},
		{
			name:     "flattened args at top level",
			raw:      `{"tool":"read_file","path":"main.go","done":false,"reason":"entry"}`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "main.go"},
		},
		{
			name:     "unquoted keys",
			raw:      `{tool: "search", args: {query: "main"}, done: false, reason: "find"}`,
			wantTool: contracts.ToolSearch,
			wantArgs: map[string]any{"query": "main"},
		},
		{
			name:     "truncated generation",
			raw:      `{"tool":"read_file","args":{"path":"main.go"`,
			wantTool: contracts.ToolReadFile,
			wantArgs: map[string]any{"path": "main.go"},
		},
		{
			name: "two objects, first wins",
			raw: `{"tool":"read_file","args":{"path":"a.go"},"done":false,"reason":"first"} ` +
				`{"tool":"list_dir","args":{},"done":false,"reason":"second"}`,
			wantTool:   contracts.ToolReadFile,
			wantReason: "first",
		},
		{
			name:     "backticked tool name",
			raw:      "{\"tool\":\"`read_file`\",\"args\":{},\"done\":false,\"reason\":\"x\"}",
			wantTool: contracts.ToolReadFile,
		},
		{
			name:     "multiline reason with raw newline",
			raw:      "{\"tool\":\"read_file\",\"args\":{\"path\":\"a.go\"},\"done\":false,\"reason\":\"first line\nsecond line\"}",
			wantTool: contracts.ToolReadFile,
		},
		{
			name:     "tool name means stop",
			raw:      `{"tool":"stop","args":{},"done":false,"reason":"seen enough"}`,
			wantTool: "",
			wantDone: true,
		},
		{
			name:     "unknown tool is preserved for the caller to reject",
			raw:      `{"tool":"delete_everything","args":{},"done":false,"reason":"nope"}`,
			wantTool: "delete_everything",
		},
		{
			name:     "empty tool with done false parses successfully",
			raw:      `{"tool":"","args":{},"done":false,"reason":"confused"}`,
			wantTool: "",
			wantDone: false,
		},
		{
			name:     "object with no recognised keys still parses",
			raw:      `{"foo":"bar"}`,
			wantTool: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDecision(tc.raw)
			if err != nil {
				t.Fatalf("ParseDecision(%q) returned error: %v", tc.raw, err)
			}
			if got.Tool != tc.wantTool {
				t.Errorf("Tool = %q, want %q", got.Tool, tc.wantTool)
			}
			if got.Done != tc.wantDone {
				t.Errorf("Done = %v, want %v", got.Done, tc.wantDone)
			}
			if got.Args == nil {
				t.Error("Args must never be nil")
			}
			if tc.wantArgs != nil {
				if len(got.Args) != len(tc.wantArgs) {
					t.Errorf("Args = %#v, want %#v", got.Args, tc.wantArgs)
				}
				for k, v := range tc.wantArgs {
					if gv, ok := got.Args[k]; !ok || gv != v {
						t.Errorf("Args[%q] = %#v (present=%v), want %#v", k, gv, ok, v)
					}
				}
			}
			if tc.wantReason != "" && got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if strings.ContainsAny(got.Reason, "\n\r") {
				t.Errorf("Reason must be a single line, got %q", got.Reason)
			}
		})
	}
}

// TestParseDecisionErrors covers input with nothing object-shaped in it.
func TestParseDecisionErrors(t *testing.T) {
	for _, raw := range []string{
		"",
		"   \n\t ",
		"I'm sorry, I cannot help with that request.",
		"The answer is 42.",
		"```json\nno object here\n```",
	} {
		dec, err := ParseDecision(raw)
		if err == nil {
			t.Errorf("ParseDecision(%q) = %#v, want an error", raw, dec)
			continue
		}
		if dec.Args == nil {
			t.Errorf("ParseDecision(%q) returned nil Args alongside the error", raw)
		}
	}
}

// TestNormaliseToolName pins the misspelling/near-miss mapping.
func TestNormaliseToolName(t *testing.T) {
	cases := map[string]string{
		"read_file":        contracts.ToolReadFile,
		"readfile":         contracts.ToolReadFile,
		"read-file":        contracts.ToolReadFile,
		"Read File":        contracts.ToolReadFile,
		"READ_FILE":        contracts.ToolReadFile,
		"open_file":        contracts.ToolReadFile,
		"cat":              contracts.ToolReadFile,
		"  read_file  ":    contracts.ToolReadFile,
		"\"read_file\"":    contracts.ToolReadFile,
		"read_file.":       contracts.ToolReadFile,
		"functions.cat":    contracts.ToolReadFile,
		"read_file(path)":  contracts.ToolReadFile,
		"ls":               contracts.ToolListDir,
		"listdir":          contracts.ToolListDir,
		"list_directory":   contracts.ToolListDir,
		"list_dir":         contracts.ToolListDir,
		"grep":             contracts.ToolSearch,
		"find":             contracts.ToolSearch,
		"search":           contracts.ToolSearch,
		"imports":          contracts.ToolFollowImports,
		"follow-imports":   contracts.ToolFollowImports,
		"follow_imports":   contracts.ToolFollowImports,
		"tree":             contracts.ToolRepoTree,
		"repo-tree":        contracts.ToolRepoTree,
		"repotree":         contracts.ToolRepoTree,
		"repo_tree":        contracts.ToolRepoTree,
		"read_file_tool":   contracts.ToolReadFile,
		"":                 "",
		"delete_the_world": "delete_the_world",
	}
	for in, want := range cases {
		if got := NormaliseToolName(in); got != want {
			t.Errorf("NormaliseToolName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRepairJSONKeepsValidInputIntact is a sanity check on the repairer: it
// must be a no-op (semantically) for already-valid JSON.
func TestRepairJSONKeepsValidInputIntact(t *testing.T) {
	in := `{"tool":"read_file","args":{"path":"a.go","n":12,"x":1.5e3,"ok":true,"z":null},"done":false}`
	out := repairJSON(in)
	m, ok := unmarshalObject(out)
	if !ok {
		t.Fatalf("repairJSON broke valid JSON: %s", out)
	}
	if m["tool"] != "read_file" {
		t.Errorf("tool = %#v after repair", m["tool"])
	}
	args, _ := m["args"].(map[string]any)
	if args["n"] != float64(12) || args["x"] != float64(1500) {
		t.Errorf("numbers mangled by repair: %#v", args)
	}
}
