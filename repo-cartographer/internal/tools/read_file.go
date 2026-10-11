package tools

import (
	"fmt"
	"os"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

const (
	// defaultReadBytes is how much of a file read_file hands the model when it
	// does not ask for a specific budget.
	defaultReadBytes = 16000
	// maxReadBytes caps what a model may ask for, however enthusiastic it is.
	maxReadBytes = 4 << 20
	// truncationMarker is appended to truncated file text so the model knows
	// it is not seeing the whole file.
	truncationMarker = "\n... [truncated]"
)

// readFileTool implements contracts.Tool for "read_file".
type readFileTool struct{ sb *sandbox }

func (t *readFileTool) Name() string { return contracts.ToolReadFile }

func (t *readFileTool) Description() string {
	return `read_file: read one file from the repo. args: {"path":"relative/path.go"}; optional {"max_bytes":16000}`
}

// Call reads one file.
//
// Meta: "path" string, "lang" string, "size" int (full size on disk),
// "truncated" bool, "imports" []string.
func (t *readFileTool) Call(args map[string]any) (contracts.ToolResult, error) {
	raw, ok := argString(args, pathAliases...)
	if !ok {
		return contracts.ToolResult{}, missingArgErr(contracts.ToolReadFile, "path", `{"path":"main.go"}`)
	}
	abs, rel, err := t.sb.resolve(raw)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: %w", contracts.ToolReadFile, err)
	}

	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return contracts.ToolResult{}, fmt.Errorf("%s: no such file: %s", contracts.ToolReadFile, rel)
		}
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot stat %s: %w", contracts.ToolReadFile, rel, errNoPath(err))
	}
	if info.IsDir() {
		return contracts.ToolResult{}, fmt.Errorf("%s: %s is a directory, use list_dir", contracts.ToolReadFile, rel)
	}

	limit := clampInt(argInt(args, defaultReadBytes, "max_bytes", "maxBytes", "max_size", "limit"),
		defaultReadBytes, 1, maxReadBytes)

	data, size, err := readLimited(abs, limit)
	if err != nil {
		return contracts.ToolResult{}, fmt.Errorf("%s: cannot read %s: %w", contracts.ToolReadFile, rel, errNoPath(err))
	}
	if looksBinary(data) {
		return contracts.ToolResult{}, fmt.Errorf("%s: %s looks like a binary file", contracts.ToolReadFile, rel)
	}

	text := string(data)
	truncated := size > int64(len(data))
	if truncated {
		text += truncationMarker
	}

	imports := extractImports(string(data))
	if imports == nil {
		imports = []string{}
	}

	return contracts.ToolResult{
		Text: text,
		Meta: map[string]any{
			"path":      rel,
			"lang":      DetectLang(rel),
			"size":      int(size),
			"truncated": truncated,
			"imports":   imports,
		},
	}, nil
}

// errNoPath strips the host path out of an *os.PathError so that tool errors,
// which are shown to the model, never leak absolute filesystem locations.
func errNoPath(err error) error {
	if err == nil {
		return nil
	}
	if pe, ok := err.(*os.PathError); ok {
		return fmt.Errorf("%s: %v", pe.Op, pe.Err)
	}
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i > 0 && strings.Contains(msg, "/") {
		return fmt.Errorf("%s", msg[i+2:])
	}
	return err
}
