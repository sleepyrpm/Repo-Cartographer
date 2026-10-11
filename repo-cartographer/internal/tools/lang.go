package tools

import (
	"path/filepath"
	"strings"
)

// langUnknown is the language reported for files we cannot classify. It is
// deliberately a non-empty, printable token so model prompts never show an
// empty field.
const langUnknown = "text"

// extLangs maps a lowercase file extension (with dot) to a language token.
// The tokens are stable: the report layer groups and colours nodes by them.
var extLangs = map[string]string{
	".go":         "go",
	".py":         "python",
	".pyw":        "python",
	".pyi":        "python",
	".js":         "javascript",
	".mjs":        "javascript",
	".cjs":        "javascript",
	".jsx":        "jsx",
	".ts":         "typescript",
	".mts":        "typescript",
	".cts":        "typescript",
	".tsx":        "tsx",
	".java":       "java",
	".c":          "c",
	".h":          "h",
	".hh":         "hpp",
	".hpp":        "hpp",
	".hxx":        "hpp",
	".cc":         "cpp",
	".cpp":        "cpp",
	".cxx":        "cpp",
	".rs":         "rust",
	".rb":         "ruby",
	".php":        "php",
	".sh":         "shell",
	".bash":       "shell",
	".zsh":        "shell",
	".fish":       "shell",
	".md":         "markdown",
	".markdown":   "markdown",
	".json":       "json",
	".jsonc":      "json",
	".yaml":       "yaml",
	".yml":        "yaml",
	".toml":       "toml",
	".html":       "html",
	".htm":        "html",
	".css":        "css",
	".scss":       "css",
	".sass":       "css",
	".less":       "css",
	".sql":        "sql",
	".txt":        langUnknown,
	".gradle":     "gradle",
	".dockerfile": "dockerfile",
	".mk":         "makefile",
}

// nameLangs maps whole lowercase file names (extension-less or conventionally
// named build files) to a language token. Checked before the extension table.
var nameLangs = map[string]string{
	"makefile":       "makefile",
	"gnumakefile":    "makefile",
	"dockerfile":     "dockerfile",
	"containerfile":  "dockerfile",
	"go.mod":         "gomod",
	"go.sum":         "gomod",
	"go.work":        "gomod",
	"package.json":   "json",
	"cmakelists.txt": "cmake",
	"rakefile":       "ruby",
	"gemfile":        "ruby",
}

// DetectLang classifies a file by path, using its name first and then its
// extension. Unknown files are reported as "text" (never "").
//
// The argument may be absolute, relative, slash- or backslash-separated; only
// the final element is inspected.
func DetectLang(path string) string {
	name := strings.ToLower(filepath.Base(filepath.ToSlash(strings.TrimSpace(path))))
	if name == "" || name == "." || name == "/" {
		return langUnknown
	}
	if lang, ok := nameLangs[name]; ok {
		return lang
	}
	// "Dockerfile.dev", "Makefile.local" and friends.
	if i := strings.IndexByte(name, '.'); i > 0 {
		if lang, ok := nameLangs[name[:i]]; ok {
			return lang
		}
	}
	ext := filepath.Ext(name)
	if ext == "" {
		return langUnknown
	}
	if lang, ok := extLangs[ext]; ok {
		return lang
	}
	return langUnknown
}
