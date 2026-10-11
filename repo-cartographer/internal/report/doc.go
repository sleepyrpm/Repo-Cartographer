// Package report turns everything the explore loop learned about a repository
// into the payoff artifacts a newcomer actually wants: a Mermaid architecture
// diagram, a ranked "read these files first" guide and a markdown report.
//
// The package depends only on the standard library and
// github.com/ayush-2007-iit/repo-cartographer/internal/contracts, so it can be
// rendered and tested without a model, a tool registry or a network.
//
// Everything here is deterministic: every map is sorted before it is iterated,
// so the same Input always renders byte-identical output.
package report
