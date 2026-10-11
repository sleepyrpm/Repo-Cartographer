package tools

import (
	"strings"
	"testing"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

func TestExtractImports(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "go single and grouped",
			src: `package main

import "fmt"

import (
	"os"
	alias "strings"
	_ "embed"
	"example.com/m/internal/util" // a comment
)

func main() {}
`,
			want: []string{"fmt", "os", "strings", "embed", "example.com/m/internal/util"},
		},
		{
			name: "python",
			src: `import os
import sys, json
from pathlib import Path
from .sibling import thing
from ..pkg.deep import other
import pkg.mod as pm
# import commented
`,
			want: []string{"os", "sys", "json", "pathlib", ".sibling", "..pkg.deep", "pkg.mod"},
		},
		{
			name: "javascript and typescript",
			src: `import React from "react";
import "./styles.css";
import { a, b } from '../lib/util';
const x = require("./helper");
export { y } from "./y";
const mod = await import("./dyn.js");
// import "./ignored";
`,
			want: []string{"react", "./styles.css", "../lib/util", "./helper", "./y", "./dyn.js"},
		},
		{
			name: "c and cpp",
			src: `#include <stdio.h>
#include "local.h"
#  include "../shared/types.hpp"
`,
			want: []string{"stdio.h", "local.h", "../shared/types.hpp"},
		},
		{
			name: "rust",
			src: `use std::collections::HashMap;
pub use crate::util;
use serde::{Serialize, Deserialize};
`,
			want: []string{"std::collections::HashMap", "crate::util", "serde"},
		},
		{
			name: "ruby",
			src: `require "json"
require_relative "helper"
require_relative('other')
`,
			want: []string{"json", "helper", "other"},
		},
		{
			name: "java",
			src: `package a;

import java.util.List;
import static org.junit.Assert.assertTrue;
`,
			want: []string{"java.util.List", "org.junit.Assert.assertTrue"},
		},
		{
			name: "php",
			src: `<?php
use App\Models\User;
require_once "bootstrap.php";
`,
			want: []string{`App\Models\User`, "bootstrap.php"},
		},
		{
			name: "deduplicates and keeps file order",
			src: `import "fmt"
import "os"
import "fmt"
`,
			want: []string{"fmt", "os"},
		},
		{
			name: "no imports",
			src:  "package main\n\nfunc main() {}\n",
			want: nil,
		},
		{
			name: "empty",
			src:  "",
			want: nil,
		},
		{
			name: "crlf line endings",
			src:  "import \"fmt\"\r\nimport \"os\"\r\n",
			want: []string{"fmt", "os"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractImports(tc.src)
			if len(tc.want) == 0 {
				if len(got) != 0 {
					t.Fatalf("extractImports = %v, want none", got)
				}
				return
			}
			if !equalStrings(got, tc.want) {
				t.Errorf("extractImports = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeSpec(t *testing.T) {
	tests := []struct{ in, want string }{
		{"fmt", "fmt"},
		{"example.com/m/internal/util", "example.com/m/internal/util"},
		{"pkg.mod", "pkg/mod"},
		{"java.util.List", "java/util/List"},
		{"std::collections::HashMap", "std/collections/HashMap"},
		{"crate::util", "util"},
		{"self::util", "util"},
		{`App\Models\User`, "App/Models/User"},
		{"./helper", "./helper"},
		{"../lib/util", "../lib/util"},
		{".sibling", "sibling"},
		{"..pkg.deep", "../pkg/deep"},
		{"local.h", "local.h"},
		{"config.json", "config.json"},
		{"  \"fmt\"  ", "fmt"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizeSpec(tc.in); got != tc.want {
			t.Errorf("normalizeSpec(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSpecTails(t *testing.T) {
	got := specTails("example.com/m/internal/util")
	want := []string{"example.com/m/internal/util", "m/internal/util", "internal/util", "util"}
	if !equalStrings(got, want) {
		t.Errorf("specTails = %v, want %v", got, want)
	}
	if got := specTails("local.h"); !equalStrings(got, []string{"local.h"}) {
		t.Errorf("specTails(single segment) = %v, want [local.h]", got)
	}
}

func TestFollowImportsResolvesRealFiles(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		target       string
		wantImports  []string
		wantResolved []string
	}{
		{
			name: "go package imports resolve to package files",
			files: map[string]string{
				"go.mod": "module example.com/app\n",
				"main.go": `package main

import (
	"fmt"
	"example.com/app/internal/util"
	"example.com/app/pkg/deep"
)
`,
				"internal/util/util.go":   "package util\n",
				"internal/util/helper.go": "package util\n",
				"pkg/deep/deep.go":        "package deep\n",
			},
			target:       "main.go",
			wantImports:  []string{"fmt", "example.com/app/internal/util", "example.com/app/pkg/deep"},
			wantResolved: []string{"internal/util/helper.go", "internal/util/util.go", "pkg/deep/deep.go"},
		},
		{
			name: "python relative and package imports",
			files: map[string]string{
				"pkg/main.py": `import os
from .sibling import thing
from pkg.mod import x
import json
`,
				"pkg/sibling.py": "thing = 1\n",
				"pkg/mod.py":     "x = 1\n",
			},
			target:       "pkg/main.py",
			wantImports:  []string{"os", ".sibling", "pkg.mod", "json"},
			wantResolved: []string{"pkg/sibling.py", "pkg/mod.py"},
		},
		{
			name: "python package under src resolves by tail",
			files: map[string]string{
				"src/app/main.py":   "from app.models import User\n",
				"src/app/models.py": "class User: pass\n",
			},
			target:       "src/app/main.py",
			wantImports:  []string{"app.models"},
			wantResolved: []string{"src/app/models.py"},
		},
		{
			name: "javascript relative, directory index and bare module",
			files: map[string]string{
				"app/index.js": `import helper from "./helper";
import sub from "./sub";
const t = require("../lib/thing");
import react from "react";
`,
				"app/helper.js":    "export default 1\n",
				"app/sub/index.js": "export default 2\n",
				"lib/thing.js":     "module.exports = 3\n",
			},
			target:       "app/index.js",
			wantImports:  []string{"./helper", "./sub", "../lib/thing", "react"},
			wantResolved: []string{"app/helper.js", "app/sub/index.js", "lib/thing.js"},
		},
		{
			name: "typescript extensionless import",
			files: map[string]string{
				"src/main.ts":   "import { h } from './helper';\n",
				"src/helper.ts": "export const h = 1;\n",
			},
			target:       "src/main.ts",
			wantImports:  []string{"./helper"},
			wantResolved: []string{"src/helper.ts"},
		},
		{
			name: "c includes",
			files: map[string]string{
				"src/main.c": `#include <stdio.h>
#include "local.h"
#include "../include/types.h"
`,
				"src/local.h":     "#pragma once\n",
				"include/types.h": "#pragma once\n",
			},
			target:       "src/main.c",
			wantImports:  []string{"stdio.h", "local.h", "../include/types.h"},
			wantResolved: []string{"src/local.h", "include/types.h"},
		},
		{
			name: "rust crate module",
			files: map[string]string{
				"src/main.rs": `use std::collections::HashMap;
use crate::util;
`,
				"src/util.rs": "pub fn x() {}\n",
			},
			target:       "src/main.rs",
			wantImports:  []string{"std::collections::HashMap", "crate::util"},
			wantResolved: []string{"src/util.rs"},
		},
		{
			name: "python __init__ entry point",
			files: map[string]string{
				"main.py":         "import pkg\n",
				"pkg/__init__.py": "from . import thing\n",
			},
			target:       "main.py",
			wantImports:  []string{"pkg"},
			wantResolved: []string{"pkg/__init__.py"},
		},
		{
			name: "self import is not an edge",
			files: map[string]string{
				"a/main.js": "const self = require('./main');\n",
			},
			target:       "a/main.js",
			wantImports:  []string{"./main"},
			wantResolved: nil,
		},
		{
			name: "imports that leave the repo are dropped",
			files: map[string]string{
				"a/main.js": "const x = require('../../../etc/passwd');\nimport y from 'left-pad';\n",
			},
			target:       "a/main.js",
			wantImports:  []string{"../../../etc/passwd", "left-pad"},
			wantResolved: nil,
		},
		{
			name: "imports pointing into skipped directories are dropped",
			files: map[string]string{
				"a/main.js":                 "const dep = require('../node_modules/dep');\n",
				"node_modules/dep/index.js": "module.exports = 1\n",
			},
			target:       "a/main.js",
			wantImports:  []string{"../node_modules/dep"},
			wantResolved: nil,
		},
		{
			name: "file without imports",
			files: map[string]string{
				"README.md": "# hello\n",
			},
			target:       "README.md",
			wantImports:  nil,
			wantResolved: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := newRepo(t, tc.files)
			tool := mustTool(t, root, contracts.ToolFollowImports)
			res, err := tool.Call(map[string]any{"path": tc.target})
			if err != nil {
				t.Fatalf("follow_imports(%q): %v", tc.target, err)
			}

			gotImports := metaStrings(t, res, "imports")
			if len(tc.wantImports) == 0 {
				if len(gotImports) != 0 {
					t.Errorf("imports = %v, want none", gotImports)
				}
			} else if !equalStrings(gotImports, tc.wantImports) {
				t.Errorf("imports = %v, want %v", gotImports, tc.wantImports)
			}

			gotResolved := metaStrings(t, res, "resolved")
			if len(tc.wantResolved) == 0 {
				if len(gotResolved) != 0 {
					t.Errorf("resolved = %v, want none", gotResolved)
				}
			} else if !equalStrings(gotResolved, tc.wantResolved) {
				t.Errorf("resolved = %v, want %v", gotResolved, tc.wantResolved)
			}

			// Every resolved path must name a file that really exists in the repo.
			for _, r := range gotResolved {
				if !mustExist(t, root, r) {
					t.Errorf("resolved %q does not exist in the repo", r)
				}
			}
			if strings.Contains(res.Text, root) {
				t.Errorf("text leaks the absolute repo root: %q", res.Text)
			}
		})
	}
}

func TestFollowImportsTextAndErrors(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go":     "package main\n\nimport \"example.com/app/pkg\"\n",
		"pkg/pkg.go":  "package pkg\n",
		"plain.txt":   "nothing to import\n",
		"blob.bin":    "a\x00b",
		"sub/keep.go": "package sub\n",
	})
	tool := mustTool(t, root, contracts.ToolFollowImports)

	res, err := tool.Call(map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "example.com/app/pkg") || !strings.Contains(res.Text, "pkg/pkg.go") {
		t.Errorf("text = %q, want it to list the spec and the resolved file", res.Text)
	}

	res, err = tool.Call(map[string]any{"path": "plain.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "imports nothing") {
		t.Errorf("text = %q, want an explicit no-imports message", res.Text)
	}

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"nil args", nil},
		{"no path", map[string]any{}},
		{"missing file", map[string]any{"path": "nope.go"}},
		{"directory", map[string]any{"path": "sub"}},
		{"binary", map[string]any{"path": "blob.bin"}},
		{"escape", map[string]any{"path": "../../etc/passwd"}},
	} {
		if _, err := tool.Call(tc.args); err == nil {
			t.Errorf("%s: want error, got none", tc.name)
		}
	}
}

// TestResolvedEdgePrecision locks in the rule that keeps the diagram honest: a
// trimmed single-segment tail may only match a directory, never an arbitrary
// same-named file elsewhere in the repo.
func TestResolvedEdgePrecision(t *testing.T) {
	root := newRepo(t, map[string]string{
		"src/main.rs":   "use util::helper;\n",
		"web/helper.js": "export default 1\n",
	})
	res, err := mustTool(t, root, contracts.ToolFollowImports).Call(map[string]any{"path": "src/main.rs"})
	if err != nil {
		t.Fatal(err)
	}
	if got := metaStrings(t, res, "resolved"); len(got) != 0 {
		t.Errorf("resolved = %v, want none: web/helper.js is not what util::helper means", got)
	}
}

// TestReadFileImportsMatchFollowImports pins the contract that read_file's
// Meta["imports"] uses the same extraction as follow_imports.
func TestReadFileImportsMatchFollowImports(t *testing.T) {
	root := newRepo(t, map[string]string{
		"main.go": "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n",
	})
	read := mustTool(t, root, contracts.ToolReadFile)
	follow := mustTool(t, root, contracts.ToolFollowImports)

	r1, err := read.Call(map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := follow.Call(map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if a, b := metaStrings(t, r1, "imports"), metaStrings(t, r2, "imports"); !equalStrings(a, b) {
		t.Errorf("read_file imports = %v, follow_imports imports = %v, want identical", a, b)
	}
}

// TestResolvedAcrossNestedModule covers the case where the repo root is not the
// module root, so a Go import path's prefix cannot be stripped by name: the
// package directory has to be found by its tail.
func TestResolvedAcrossNestedModule(t *testing.T) {
	root := newRepo(t, map[string]string{
		"sample/go.mod":                    "module example.com/pulse\n",
		"sample/cmd/pulse/main.go":         "package main\n\nimport (\n\t\"fmt\"\n\t\"example.com/pulse/internal/digest\"\n)\n",
		"sample/internal/digest/digest.go": "package digest\n",
	})
	res, err := mustTool(t, root, contracts.ToolFollowImports).Call(map[string]any{"path": "sample/cmd/pulse/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sample/internal/digest/digest.go"}
	if got := metaStrings(t, res, "resolved"); !equalStrings(got, want) {
		t.Errorf("resolved = %v, want %v", got, want)
	}
}
