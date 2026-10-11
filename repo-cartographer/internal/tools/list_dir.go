package tools

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// listDirTool implements contracts.Tool for "list_dir".
type listDirTool struct{ sb *sandbox }

func (t *listDirTool) Name() string { return contracts.ToolListDir }

func (t *listDirTool) Description() string {
	return `list_dir: list the entries of one directory. args: {"path":"."} (optional, default repo root; directories end in "/")`
}

// Call lists one directory. Skip-listed names (.git, node_modules, ...) are
// omitted so the model never wanders into dependency trees.
//
// Meta: "entries" []string (directories carry a trailing "/"), "path" string.
func (t *listDirTool) Call(args map[string]any) (contracts.ToolResult, error) {
	raw, _ := argString(args, pathAliases...) // optional: "" means repo root
	abs, rel, err := t.sb.resolve(raw)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: %w", contracts.ToolListDir, err)
	}

	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return contracts.ToolResult{}, fmt.Errorf("%s: no such directory: %s", contracts.ToolListDir, rel)
		}
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot stat %s: %w", contracts.ToolListDir, rel, errNoPath(err))
	}
	if !info.IsDir() {
		return contracts.ToolResult{}, fmt.Errorf("%s: %s is a file, use read_file", contracts.ToolListDir, rel)
	}

	raws, err := os.ReadDir(abs)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot list %s: %w", contracts.ToolListDir, rel, errNoPath(err))
	}

	dirs := make([]string, 0, len(raws))
	files := make([]string, 0, len(raws))
	for _, e := range raws {
		name := e.Name()
		if isSkippedName(name) {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, name+"/")
			continue
		}
		// A symlink to a directory is still shown as a plain entry; following
		// it is the sandbox's problem, not this listing's.
		files = append(files, name)
	}
	sort.Strings(dirs)
	sort.Strings(files)
	entries := append(dirs, files...)

	text := strings.Join(entries, "\n")
	if len(entries) == 0 {
		text = fmt.Sprintf("(%s is empty)", rel)
	}

	return contracts.ToolResult{
		Text: text,
		Meta: map[string]any{
			"entries": entries,
			"path":    rel,
		},
	}, nil
}
