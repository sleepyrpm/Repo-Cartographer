package tools

import (
	"fmt"

	"github.com/ayush-2007-iit/repo-cartographer/internal/contracts"
)

// registry is the immutable contracts.Registry returned by New. It keeps a
// stable tool order (the order a reader would naturally use them in) plus a
// name index.
type registry struct {
	order  []contracts.Tool
	byName map[string]contracts.Tool
}

// New returns a Registry holding all five tools, sandboxed to root.
//
// root must exist and be a directory. Every tool in the returned registry
// resolves paths through one shared sandbox, so none of them can read outside
// root, and none of them emit host-absolute paths.
func New(root string) (contracts.Registry, error) {
	sb, err := newSandbox(root)
	if err != nil {
		return nil, fmt.Errorf("tools: %w", err)
	}
	order := []contracts.Tool{
		&repoTreeTool{sb: sb},
		&listDirTool{sb: sb},
		&readFileTool{sb: sb},
		&searchTool{sb: sb},
		&followImportsTool{sb: sb},
	}
	byName := make(map[string]contracts.Tool, len(order))
	for _, t := range order {
		byName[t.Name()] = t
	}
	return &registry{order: order, byName: byName}, nil
}

// Get returns the tool registered under name.
func (r *registry) Get(name string) (contracts.Tool, bool) {
	t, ok := r.byName[cleanPathArg(name)]
	return t, ok
}

// List returns every tool, in a stable order.
func (r *registry) List() []contracts.Tool {
	out := make([]contracts.Tool, len(r.order))
	copy(out, r.order)
	return out
}

// Describe returns the name and description of every tool, in List order. This
// is what the model is shown each step, so the descriptions are short and state
// their exact JSON argument names.
func (r *registry) Describe() []contracts.ToolInfo {
	out := make([]contracts.ToolInfo, 0, len(r.order))
	for _, t := range r.order {
		out = append(out, contracts.ToolInfo{Name: t.Name(), Description: t.Description()})
	}
	return out
}
