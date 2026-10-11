package tools

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

const (
	// maxFileBytes is the largest file the walkers will treat as source. Bigger
	// files are generated artifacts, fixtures or blobs; reading them would just
	// burn the model's context.
	maxFileBytes = 1 << 20 // 1 MiB

	// binarySniffBytes is how much of a file head is inspected for NUL bytes
	// when deciding whether a file is binary.
	binarySniffBytes = 512
)

// skipNames are directory and file names that are never worth exploring: VCS
// metadata, dependency caches, build output and editor state. One list, used by
// every tool, so list_dir, search and repo_tree all agree on what exists.
var skipNames = []string{
	".git",
	"node_modules",
	"vendor",
	"dist",
	"build",
	"target",
	".venv",
	"venv",
	"__pycache__",
	".idea",
	".vscode",
	".next",
	".cache",
}

var skipSet = func() map[string]struct{} {
	m := make(map[string]struct{}, len(skipNames))
	for _, n := range skipNames {
		m[n] = struct{}{}
	}
	return m
}()

// isSkippedName reports whether a single path element is on the skip list.
func isSkippedName(name string) bool {
	_, ok := skipSet[name]
	return ok
}

// isSkippedPath reports whether any element of a repo-relative slash path is on
// the skip list, i.e. whether the path lives inside skipped territory.
func isSkippedPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part != "" && isSkippedName(part) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Sandbox
// ---------------------------------------------------------------------------

// sandbox confines every filesystem access to one repository root. All five
// tools share one instance; it is immutable after construction and therefore
// safe for concurrent use.
type sandbox struct {
	root     string // absolute, cleaned repo root
	realRoot string // root with symlinks resolved, for symlink containment checks
}

// newSandbox validates root and returns a sandbox anchored to it.
func newSandbox(root string) (*sandbox, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("repo root must not be empty")
	}
	abs, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("resolving repo root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("repo root is unreadable: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repo root is not a directory: %s", filepath.Base(abs))
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		real = abs
	}
	return &sandbox{root: abs, realRoot: real}, nil
}

// escapeErr is the single error shape used for every sandbox violation. The
// message quotes the path exactly as the caller supplied it so a model can see
// what it did wrong, but never leaks the host-absolute repo root.
func escapeErr(p string) error {
	return fmt.Errorf("path escapes repo root: %s", p)
}

// resolve maps a model-supplied path onto a real path inside the sandbox.
//
// It accepts "", ".", "./x", "x/y" and leading-slash forms (which are read as
// root-relative when they do not already point inside the root). It rejects
// `..` escapes, absolute paths pointing outside the root, and paths that reach
// outside via symlinks.
//
// abs is the host path to use for I/O; rel is the repo-relative, slash
// separated path to use in all output.
func (s *sandbox) resolve(raw string) (abs string, rel string, err error) {
	p := cleanPathArg(raw)
	if p == "" {
		p = "."
	}

	if filepath.IsAbs(p) {
		clean := filepath.Clean(p)
		if _, ok := containedIn(s.root, clean); ok {
			abs = clean
		} else {
			// Models habitually write "/main.go" meaning "main.go". Honour that,
			// but only when the root-relative reading names something that really
			// exists; otherwise this was a genuine attempt to leave the repo.
			cand := filepath.Join(s.root, strings.TrimLeft(filepath.ToSlash(clean), "/"))
			if _, ok := containedIn(s.root, cand); !ok {
				return "", "", escapeErr(p)
			}
			if _, statErr := os.Lstat(cand); statErr != nil {
				return "", "", escapeErr(p)
			}
			abs = cand
		}
	} else {
		abs = filepath.Join(s.root, p)
	}

	r, ok := containedIn(s.root, abs)
	if !ok {
		return "", "", escapeErr(p)
	}
	if err := s.checkSymlinks(abs, p); err != nil {
		return "", "", err
	}
	return abs, r, nil
}

// checkSymlinks walks up from abs to the longest existing prefix, resolves that
// prefix's symlinks and verifies the result still lies under the root. This
// catches `link -> /etc` style escapes while still allowing paths that do not
// exist yet (so callers can report a plain "no such file" error).
func (s *sandbox) checkSymlinks(abs, raw string) error {
	cur := abs
	var tail []string
	for i := 0; i < 256; i++ {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			full := real
			if len(tail) > 0 {
				full = filepath.Join(append([]string{real}, tail...)...)
			}
			if _, ok := containedIn(s.realRoot, full); !ok {
				return escapeErr(raw)
			}
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			// ELOOP, EACCES and friends: treat as unusable rather than allowed.
			return fmt.Errorf("cannot resolve path %s: %w", raw, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return escapeErr(raw)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
	return escapeErr(raw)
}

// containedIn reports whether p is root itself or lies beneath it, returning
// the slash-separated relative path ("." for root itself).
func containedIn(root, p string) (string, bool) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// rel converts a host-absolute path under the root into its repo-relative,
// slash-separated form. Paths outside the root come back as "" so that nothing
// can accidentally print a host path.
func (s *sandbox) rel(abs string) string {
	r, ok := containedIn(s.root, abs)
	if !ok {
		return ""
	}
	return r
}

// abs joins a repo-relative path onto the root. Callers must have validated the
// path through resolve first.
func (s *sandbox) abs(rel string) string {
	if rel == "" || rel == "." {
		return s.root
	}
	return filepath.Join(s.root, filepath.FromSlash(rel))
}

// files enumerates every non-skipped source file in the repo. It walks on every
// call rather than caching, so tools always see the repo as it is now.
func (s *sandbox) files() ([]contracts.RepoFile, error) {
	files, _, err := walkTree(s.root, s.root)
	return files, err
}

// ---------------------------------------------------------------------------
// Walking
// ---------------------------------------------------------------------------

// walkTree enumerates the source files and directories under dir, reporting
// paths relative to relTo (slash-separated, sorted). Skip-listed directories
// are pruned; oversized, binary, non-regular and symlinked files are omitted.
// Unreadable entries are skipped rather than failing the whole walk.
func walkTree(dir, relTo string) (files []contracts.RepoFile, dirs []string, err error) {
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if path != dir && isSkippedName(name) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		relPath, ok := containedIn(relTo, path)
		if !ok {
			return nil
		}
		if d.IsDir() {
			if path != dir && relPath != "." {
				dirs = append(dirs, relPath)
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			// Symlinks could point outside the root; irregular files (sockets,
			// devices) are not source.
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil || info.Size() > maxFileBytes {
			return nil
		}
		if looksBinaryFile(path) {
			return nil
		}
		files = append(files, contracts.RepoFile{
			Path: relPath,
			Lang: DetectLang(relPath),
			Size: int(info.Size()),
		})
		return nil
	})
	if walkErr != nil {
		return nil, nil, walkErr
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(dirs)
	return files, dirs, nil
}

// looksBinaryFile reports whether a file's first bytes contain a NUL, the cheap
// heuristic every "is this text?" check in this package uses.
func looksBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true // unreadable: treat as not-source
	}
	defer f.Close()
	buf := make([]byte, binarySniffBytes)
	n, err := io.ReadFull(f, buf)
	if n == 0 && err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return looksBinary(buf[:n])
}

// looksBinary reports whether an in-memory buffer looks binary.
func looksBinary(b []byte) bool {
	if len(b) > binarySniffBytes {
		b = b[:binarySniffBytes]
	}
	return bytes.IndexByte(b, 0) >= 0
}

// readLimited reads at most limit bytes of path and reports the file's full
// size on disk, so callers can tell the model that it was truncated.
func readLimited(path string, limit int) (data []byte, size int64, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	if info.IsDir() {
		return nil, 0, fmt.Errorf("is a directory")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	if limit < 0 {
		limit = 0
	}
	data, err = io.ReadAll(io.LimitReader(f, int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	return data, info.Size(), nil
}

// ---------------------------------------------------------------------------
// Exported helpers
// ---------------------------------------------------------------------------

// SourceFiles enumerates every non-skipped source file under root, with
// repo-relative slash paths, detected language and byte size, sorted by path.
//
// It applies the same skip list, size cap and binary sniff as the tools, so the
// agent's file inventory and the tools' view of the repo cannot disagree.
func SourceFiles(root string) ([]contracts.RepoFile, error) {
	sb, err := newSandbox(root)
	if err != nil {
		return nil, err
	}
	return sb.files()
}
