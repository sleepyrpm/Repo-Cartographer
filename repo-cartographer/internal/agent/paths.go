package agent

import (
	"path"
	"path/filepath"
	"strings"
)

// Path hygiene. The tools already sandbox their filesystem access; this is
// defence in depth for the paths the agent *stores*, so a hallucinated
// "/etc/passwd" or "../../secrets.env" can never end up in the Result, the
// report or the diagram.
//
// Every stored path is repo-relative, slash-separated and clean.

// normPath normalises a model- or tool-supplied path and reports whether it is
// acceptable. "." is allowed (it means the repo root, which list_dir accepts).
func normPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	// Accept Windows-style separators from a model and normalise them; a
	// backslash is never meaningful in a repo-relative path we want to store.
	p = strings.ReplaceAll(filepath.ToSlash(p), `\`, "/")
	// Absolute paths (POSIX or Windows drive/UNC) are out of bounds.
	if strings.HasPrefix(p, "/") {
		return "", false
	}
	if len(p) >= 2 && p[1] == ':' {
		return "", false
	}
	// Reject anything with a parent reference before cleaning, so a path like
	// "a/../../b" that cleans to "../b" cannot sneak through.
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	p = path.Clean(p)
	if p == "" || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// normFile is normPath for things that must name a file: the repo root and
// trailing-slash directory spellings are rejected.
func normFile(p string) (string, bool) {
	if strings.HasSuffix(strings.TrimSpace(p), "/") {
		return "", false
	}
	n, ok := normPath(p)
	if !ok || n == "." {
		return "", false
	}
	return n, true
}

// normDir is normPath for directories; an empty path means the repo root.
func normDir(p string) (string, bool) {
	if strings.TrimSpace(p) == "" {
		return ".", true
	}
	return normPath(p)
}

// depth is how many directory levels deep a repo-relative path sits.
func depth(p string) int {
	if p == "." {
		return 0
	}
	return strings.Count(p, "/")
}
