package tools

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Import handling is deliberately language-agnostic: one pass of regexes over
// the file's lines, covering the languages a reader of an unknown repo is
// likely to meet. The goal is not a parser but a dependency sketch good enough
// to draw edges in a diagram.

var (
	// Go: `import "fmt"`, `import alias "x"`, `import _ "x"`.
	reGoImport = regexp.MustCompile(`^\s*import\s+(?:[\w.]+\s+|_\s+|\.\s+)?"([^"]+)"`)
	// Grouped Go block: `import (` then `alias "x"` or just `"x"` per line.
	reGoBlockOpen  = regexp.MustCompile(`^\s*import\s*\(\s*$`)
	reGoBlockEntry = regexp.MustCompile(`^\s*(?:[\w.]+\s+|_\s+|\.\s+)?"([^"]+)"\s*(?://.*)?$`)

	// Python: `import a.b, c as d` — anchored to end of statement so that a JS
	// `import x from "y"` cannot match it — and `from a.b import c`.
	rePyImport = regexp.MustCompile(`^\s*import\s+([\w.]+(?:\s+as\s+\w+)?(?:\s*,\s*[\w.]+(?:\s+as\s+\w+)?)*)\s*(?:#.*)?$`)
	rePyFrom   = regexp.MustCompile(`^\s*from\s+([.\w]+)\s+import\s+`)

	// JS/TS: `import ... from "x"`, `export ... from "x"`, bare `import "x"`,
	// `require("x")` and dynamic `import("x")`.
	reJSFrom    = regexp.MustCompile(`\bfrom\s*['"]([^'"]+)['"]`)
	reJSBare    = regexp.MustCompile(`^\s*import\s+['"]([^'"]+)['"]`)
	reJSRequire = regexp.MustCompile(`\brequire\s*\(\s*['"]([^'"]+)['"]\s*\)`)
	reJSDynamic = regexp.MustCompile(`\bimport\s*\(\s*['"]([^'"]+)['"]\s*\)`)

	// C/C++: `#include <x>` or `#include "x"`.
	reCInclude = regexp.MustCompile(`^\s*#\s*include\s*[<"]([^>"]+)[>"]`)

	// Rust / PHP: `use a::b::C;`, `pub use x;`, `use App\Thing;`.
	reUse = regexp.MustCompile(`^\s*(?:pub\s+)?use\s+([^;{]+?)\s*(?:as\s+\w+\s*)?(?:\{[^;]*\})?\s*;`)

	// Ruby / PHP: `require "x"`, `require_relative "x"`, `require_once "x"`.
	reRubyRequire = regexp.MustCompile(`^\s*require(?:_relative|_once)?\s*\(?\s*['"]([^'"]+)['"]`)

	// Java / Kotlin: `import a.b.C;`, `import static a.b.C.d;`.
	reJavaImport = regexp.MustCompile(`^\s*import\s+(?:static\s+)?([\w.*$]+)\s*;`)
)

// importExts are the extensions tried when turning an import spec into a repo
// file path, and the only extensions a resolved edge may have.
var importExts = []string{
	".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".rs",
	".c", ".h", ".cpp", ".hpp", ".java", ".rb", ".php",
}

// maxResolvedImports caps how many repo files one file's imports may resolve
// to, so a sloppy spec cannot flood the diagram.
const maxResolvedImports = 64

// extractImports pulls the raw import specs out of a file's text, deduplicated
// and in the order they appear. Specs are reported exactly as written (e.g.
// "fmt", "./util", "std::collections::HashMap") so the model sees what the file
// says; resolution to repo paths happens separately.
func extractImports(src string) []string {
	var out []string
	seen := make(map[string]struct{})
	add := func(spec string) {
		spec = strings.TrimSpace(strings.Trim(strings.TrimSpace(spec), "\"'`"))
		if spec == "" || len(spec) > 256 {
			return
		}
		if _, dup := seen[spec]; dup {
			return
		}
		seen[spec] = struct{}{}
		out = append(out, spec)
	}

	inGoBlock := false
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		// Skip obvious comment lines. `#include` and Python `#` comments are
		// handled correctly by the individual regexes.
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}

		// Grouped Go import block.
		if inGoBlock {
			if strings.HasPrefix(trimmed, ")") {
				inGoBlock = false
				continue
			}
			if m := reGoBlockEntry.FindStringSubmatch(line); m != nil {
				add(m[1])
			}
			continue
		}
		if reGoBlockOpen.MatchString(line) {
			inGoBlock = true
			continue
		}

		if m := reGoImport.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := reCInclude.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := rePyFrom.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := rePyImport.FindStringSubmatch(line); m != nil {
			for _, part := range strings.Split(m[1], ",") {
				part = strings.TrimSpace(part)
				if i := strings.Index(part, " as "); i >= 0 {
					part = strings.TrimSpace(part[:i])
				}
				add(part)
			}
		}
		if m := reJavaImport.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := reJSBare.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		for _, m := range reJSFrom.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		for _, m := range reJSRequire.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		for _, m := range reJSDynamic.FindAllStringSubmatch(line, -1) {
			add(m[1])
		}
		if m := reRubyRequire.FindStringSubmatch(line); m != nil {
			add(m[1])
		}
		if m := reUse.FindStringSubmatch(line); m != nil {
			// `use serde::{Serialize, Deserialize};` leaves a trailing "::".
			add(strings.TrimRight(m[1], ":\\/ \t"))
		}
	}
	return out
}

// resolveImports maps raw import specs onto repo-relative paths of files that
// really exist, relative to the importing file fromRel.
//
// These become the edges of the generated diagram, so precision beats recall:
// a spec that cannot be matched to an existing repo file is dropped silently.
func (s *sandbox) resolveImports(fromRel string, specs []string) []string {
	if len(specs) == 0 {
		return nil
	}
	fromDir := path.Dir(filepath.ToSlash(fromRel))
	if fromDir == "." || fromDir == "/" {
		fromDir = ""
	}

	// Index of repo file paths, built at most once per call and only when a
	// non-relative spec actually needs tail matching.
	var index []string
	indexed := false
	repoFiles := func() []string {
		if !indexed {
			indexed = true
			if files, err := s.files(); err == nil {
				index = make([]string, 0, len(files))
				for _, f := range files {
					index = append(index, f.Path)
				}
			}
		}
		return index
	}

	var out []string
	seen := make(map[string]struct{})
	add := func(rel string) {
		if rel == "" || rel == fromRel || len(out) >= maxResolvedImports {
			return
		}
		if _, dup := seen[rel]; dup {
			return
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
	}

	for _, spec := range specs {
		norm := normalizeSpec(spec)
		if norm == "" {
			continue
		}
		if strings.HasPrefix(norm, ".") {
			// Relative spec: resolve against the importing file's directory.
			for _, hit := range s.candidatesFor(path.Join(fromDir, norm)) {
				add(hit)
			}
			continue
		}
		// Package-ish spec ("pkg/mod", "pkg.mod", "a::b"): try the whole path
		// first, then progressively shorter tails, stopping at the first tail
		// that matches something real.
		//
		// A tail that has been trimmed down to a single segment is only matched
		// against directories: letting "util::helper" claim any helper.* file
		// anywhere in the repo would invent edges. The spec's own full form is
		// still matched against files, so C's `#include "local.h"` works.
		for i, cand := range specTails(norm) {
			var hits []string
			if i > 0 && !strings.Contains(cand, "/") {
				if s.isRepoDir(cand) {
					hits = s.dirSourceFiles(cand)
				}
			} else {
				hits = s.candidatesFor(cand)
				if len(hits) == 0 {
					hits = matchByTail(repoFiles(), cand)
				}
			}
			if len(hits) > 0 {
				for _, hit := range hits {
					add(hit)
				}
				break
			}
		}
	}
	return out
}

// normalizeSpec turns a language-specific spec into a slash path shape:
// `pkg.mod` -> `pkg/mod`, `a::b::C` -> `a/b/C`, `App\Thing` -> `App/Thing`,
// Python's `from .mod import x` -> `./mod`. Specs that already look like paths
// are left alone.
func normalizeSpec(spec string) string {
	spec = strings.TrimSpace(strings.Trim(strings.TrimSpace(spec), "\"'`;"))
	if spec == "" {
		return ""
	}
	if strings.HasPrefix(spec, ".") {
		if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
			return spec // already a relative path
		}
		// Python relative import: leading dots are levels up, dots inside are
		// package separators. `.mod` -> `./mod`, `..pkg.x` -> `../pkg/x`.
		lead := 0
		for lead < len(spec) && spec[lead] == '.' {
			lead++
		}
		rest := strings.ReplaceAll(spec[lead:], ".", "/")
		prefix := "./"
		for i := 1; i < lead; i++ {
			prefix += "../"
		}
		if rest == "" {
			return ""
		}
		return path.Clean(prefix + rest)
	}
	spec = strings.ReplaceAll(spec, "::", "/")
	spec = strings.ReplaceAll(spec, "\\", "/")
	if !strings.Contains(spec, "/") && strings.Contains(spec, ".") {
		// `pkg.mod.Thing` style, but keep a real trailing extension intact
		// (`config.json`, `main.py`).
		if ext := path.Ext(spec); ext == "" || !looksLikeFileExt(ext) {
			spec = strings.ReplaceAll(spec, ".", "/")
		}
	}
	spec = strings.TrimSuffix(spec, "/*")
	// Rust anchors: `crate::util`, `self::util`, `super::util` all mean "a
	// module of this project", so drop the anchor and let the tail matcher
	// find the file.
	for _, anchor := range []string{"crate/", "self/", "super/"} {
		for strings.HasPrefix(spec, anchor) {
			spec = spec[len(anchor):]
		}
	}
	return strings.Trim(spec, "/")
}

// isImportExt reports whether ext is one of the source extensions a resolved
// import edge is allowed to have.
func isImportExt(ext string) bool {
	for _, e := range importExts {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// looksLikeFileExt reports whether ext plausibly ends a file name, which is
// broader than isImportExt (it includes .json, .md, .yaml...).
func looksLikeFileExt(ext string) bool {
	return isImportExt(ext) || DetectLang("x"+ext) != langUnknown
}

// specTails yields the candidate repo paths for a package-ish spec, longest
// first: "example.com/m/internal/x" -> "example.com/m/internal/x",
// "m/internal/x", "internal/x", "x". Element 0 is always the whole spec.
//
// Trimming the leading segments is what makes a Go module path or a Python
// package root resolvable; see resolveImports for the precision rule that
// applies to the bare final segment.
func specTails(spec string) []string {
	parts := strings.Split(spec, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		if tail := strings.Join(parts[i:], "/"); tail != "" {
			out = append(out, tail)
		}
	}
	return out
}

// candidatesFor turns one repo-relative candidate path into the existing files
// it names: the path itself, the path plus a source extension, a package
// directory's source files, or an index/__init__ entry point. The result is
// empty unless something really exists inside the repo.
func (s *sandbox) candidatesFor(rel string) []string {
	rel = strings.Trim(path.Clean(filepath.ToSlash(rel)), "/")
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || isSkippedPath(rel) {
		return nil
	}

	// Exact file.
	if isImportExt(path.Ext(rel)) && s.isRepoFile(rel) {
		return []string{rel}
	}
	if path.Ext(rel) != "" && s.isRepoFile(rel) {
		return []string{rel}
	}
	// With a source extension appended.
	if path.Ext(rel) == "" {
		for _, ext := range importExts {
			if s.isRepoFile(rel + ext) {
				return []string{rel + ext}
			}
		}
	}
	// A directory: a package. Its source files are the edges.
	if s.isRepoDir(rel) {
		return s.dirSourceFiles(rel)
	}
	// `spec/index.*` and `spec/__init__.py` entry points.
	for _, ext := range importExts {
		if s.isRepoFile(rel + "/index" + ext) {
			return []string{rel + "/index" + ext}
		}
	}
	if s.isRepoFile(rel + "/__init__.py") {
		return []string{rel + "/__init__.py"}
	}
	return nil
}

// matchByTail finds the repo files a candidate path names when the repo root is
// not the module/package root: files whose path ends with the candidate
// (optionally plus a source extension), or, failing that, files in a directory
// whose path ends with the candidate.
//
// This is what lets a Go spec like "example.com/mod/internal/x" find
// "sub/module/internal/x/x.go", or a Python "pkg.mod" find "src/pkg/mod.py".
// Directory matching is restricted to multi-segment candidates: a lone word
// should not drag in every file of a same-named directory.
func matchByTail(files []string, cand string) []string {
	cand = strings.Trim(cand, "/")
	if cand == "" {
		return nil
	}
	var fileHits, dirHits []string
	for _, p := range files {
		if p == cand || strings.HasSuffix(p, "/"+cand) {
			fileHits = append(fileHits, p)
			continue
		}
		ext := path.Ext(p)
		if !isImportExt(ext) {
			continue
		}
		if path.Ext(cand) == "" {
			base := strings.TrimSuffix(p, ext)
			if base == cand || strings.HasSuffix(base, "/"+cand) {
				fileHits = append(fileHits, p)
				continue
			}
		}
		if strings.Contains(cand, "/") {
			if dir := path.Dir(p); dir == cand || strings.HasSuffix(dir, "/"+cand) {
				dirHits = append(dirHits, p)
			}
		}
	}
	if len(fileHits) > 0 {
		sort.Strings(fileHits)
		return fileHits
	}
	sort.Strings(dirHits)
	return dirHits
}

// isRepoFile reports whether rel names a readable, non-skipped regular file
// inside the sandbox.
func (s *sandbox) isRepoFile(rel string) bool {
	if rel == "" || isSkippedPath(rel) {
		return false
	}
	abs, _, err := s.resolve(rel)
	if err != nil {
		return false
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// isRepoDir reports whether rel names a non-skipped directory in the sandbox.
func (s *sandbox) isRepoDir(rel string) bool {
	if rel == "" || isSkippedPath(rel) {
		return false
	}
	abs, _, err := s.resolve(rel)
	if err != nil {
		return false
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// dirSourceFiles lists the source files directly inside a package directory,
// sorted; sub-directories and non-source files are ignored.
func (s *sandbox) dirSourceFiles(rel string) []string {
	abs, _, err := s.resolve(rel)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || isSkippedName(e.Name()) || !isImportExt(path.Ext(e.Name())) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
			continue
		}
		out = append(out, path.Join(rel, e.Name()))
	}
	sort.Strings(out)
	return out
}
