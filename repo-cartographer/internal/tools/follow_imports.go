package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// followImportsTool implements contracts.Tool for "follow_imports".
type followImportsTool struct{ sb *sandbox }

func (t *followImportsTool) Name() string { return contracts.ToolFollowImports }

func (t *followImportsTool) Description() string {
	return `follow_imports: list what one file imports and which repo files those imports point to. args: {"path":"relative/path.go"}`
}

// Call extracts a file's imports and resolves the ones that name real files
// inside the repo.
//
// Meta: "imports" []string (raw specs, deduped, in file order),
// "resolved" []string (repo-relative paths that exist in the repo).
func (t *followImportsTool) Call(args map[string]any) (contracts.ToolResult, error) {
	raw, ok := argString(args, pathAliases...)
	if !ok {
		return contracts.ToolResult{}, missingArgErr(contracts.ToolFollowImports, "path", `{"path":"main.go"}`)
	}
	abs, rel, err := t.sb.resolve(raw)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: %w", contracts.ToolFollowImports, err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return contracts.ToolResult{}, fmt.Errorf("%s: no such file: %s", contracts.ToolFollowImports, rel)
		}
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot stat %s: %w", contracts.ToolFollowImports, rel, errNoPath(err))
	}
	if info.IsDir() {
		return contracts.ToolResult{}, fmt.Errorf("%s: %s is a directory, pass one file", contracts.ToolFollowImports, rel)
	}

	data, _, err := readLimited(abs, maxFileBytes)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot read %s: %w", contracts.ToolFollowImports, rel, errNoPath(err))
	}
	if looksBinary(data) {
		return contracts.ToolResult{}, fmt.Errorf("%s: %s looks like a binary file", contracts.ToolFollowImports, rel)
	}

	imports := extractImports(string(data))
	resolved := t.sb.resolveImports(rel, imports)
	if imports == nil {
		imports = []string{}
	}
	if resolved == nil {
		resolved = []string{}
	}

	return contracts.ToolResult{
		Text: importsSummary(rel, imports, resolved),
		Meta: map[string]any{
			"imports":  imports,
			"resolved": resolved,
		},
	}, nil
}

// importsSummary renders a short, model-readable report of one file's imports.
func importsSummary(rel string, imports, resolved []string) string {
	if len(imports) == 0 {
		return fmt.Sprintf("%s imports nothing (no import statements found)", rel)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s has %d import(s):\n", rel, len(imports))
	for _, spec := range imports {
		fmt.Fprintf(&b, "  %s\n", spec)
	}
	if len(resolved) == 0 {
		b.WriteString("none of them resolve to files inside this repo (all external)")
		return b.String()
	}
	fmt.Fprintf(&b, "resolves to %d file(s) in this repo:\n", len(resolved))
	for _, r := range resolved {
		fmt.Fprintf(&b, "  -> %s\n", r)
	}
	return strings.TrimRight(b.String(), "\n")
}
