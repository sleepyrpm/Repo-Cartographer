// Package tools implements the MCP-style toolset the agent uses to investigate
// a repository: read_file, list_dir, search, follow_imports and repo_tree,
// bundled in a contracts.Registry by New.
//
// Everything in here obeys three rules, because the output is fed verbatim to a
// small local language model:
//
//  1. Sandboxing. Every path a tool touches is resolved through sandbox.resolve,
//     which refuses `..` escapes, absolute paths outside the root and symlinks
//     whose target leaves the root.
//  2. No absolute host paths in output. Text and Meta only ever carry
//     repo-relative, slash-separated paths.
//  3. Defensive argument handling. Models send `nil` args, missing keys,
//     numbers where strings belong, floats where ints belong and quoted paths.
//     A tool must return a descriptive error for unusable input and never panic.
//
// The package depends only on the standard library and internal/contracts.
package tools
