// Package graph merges manifest declarations into one dependency graph.
// Every edge remembers all of its declaration sources so that conflicts can
// always be traced back to the manifests that caused them.
package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// Edge is one aggregated requirement: from package "From" requires "To"
// satisfying all ranges in Decls.
type Edge struct {
	From  string
	To    string
	Decls []Declaration
}

// Declaration is a single requirement statement with its origin.
type Declaration struct {
	Range    semver.Range
	RawRange string
	Src      manifest.Source
}

// SatisfiedBy reports whether every declaration accepts v.
func (e *Edge) SatisfiedBy(v semver.Version) bool {
	for _, d := range e.Decls {
		if !d.Range.Contains(v) {
			return false
		}
	}
	return true
}

// Node is a package appearing in the merged graph.
type Node struct {
	Name  string
	Root  bool
	Edges []*Edge // outgoing requirements, sorted by target name
}

// Graph is the merged, deterministically ordered dependency graph.
type Graph struct {
	Roots []string
	nodes map[string]*Node
	order []string // insertion order; final graph sorts it
}

// Nodes returns all node names in lexicographic order.
func (g *Graph) Nodes() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	sort.Strings(out)
	return out
}

// Node returns the node with the given name.
func (g *Graph) Node(name string) (*Node, bool) {
	n, ok := g.nodes[name]
	return n, ok
}

// Edge returns the aggregated edge from -> to, if present.
func (g *Graph) Edge(from, to string) (*Edge, bool) {
	n, ok := g.nodes[from]
	if !ok {
		return nil, false
	}
	for _, e := range n.Edges {
		if e.To == to {
			return e, true
		}
	}
	return nil, false
}

// EdgesTo returns all declarations requiring pkg, in deterministic order
// (sorted by source package then raw range text).
func (g *Graph) EdgesTo(pkg string) []Declaration {
	var out []Declaration
	froms := g.Nodes()
	for _, from := range froms {
		if e, ok := g.Edge(from, pkg); ok {
			out = append(out, e.Decls...)
		}
	}
	return out
}

// Cycle is a detected dependency cycle.
type Cycle struct{ Path []string }

func (c Cycle) String() string { return strings.Join(c.Path, " -> ") }

// Cycles returns elementary cycles found via deterministic DFS from sorted
// root nodes. Each cycle is reported once, in its lexicographically
// smallest rotation.
func (g *Graph) Cycles() []Cycle {
	var cycles []Cycle
	seen := map[string]bool{}
	colors := map[string]int{} // 0 white, 1 gray, 2 black
	var stack []string

	var dfs func(u string)
	dfs = func(u string) {
		colors[u] = 1
		stack = append(stack, u)
		n, _ := g.nodes[u]
		if n != nil {
			targets := make([]string, 0, len(n.Edges))
			for _, e := range n.Edges {
				targets = append(targets, e.To)
			}
			sort.Strings(targets)
			for _, v := range targets {
				if colors[v] == 1 {
					// Found a back edge; extract cycle from stack.
					var cyc []string
					for i, x := range stack {
						if x == v {
							cyc = append(cyc, stack[i:]...)
							break
						}
					}
					cyc = append(cyc, v)
					key := canonicalCycleKey(cyc)
					if !seen[key] {
						seen[key] = true
						cycles = append(cycles, Cycle{Path: canonicalRotation(cyc)})
					}
				} else if colors[v] == 0 {
					dfs(v)
				}
			}
		}
		stack = stack[:len(stack)-1]
		colors[u] = 2
	}
	for _, u := range g.Nodes() {
		if colors[u] == 0 {
			dfs(u)
		}
	}
	sort.Slice(cycles, func(i, j int) bool {
		return strings.Join(cycles[i].Path, "\x00") < strings.Join(cycles[j].Path, "\x00")
	})
	return cycles
}

func canonicalCycleKey(path []string) string {
	return strings.Join(canonicalRotation(path), "\x00")
}

// canonicalRotation rotates the (closed) cycle so its smallest node leads;
// the repeated closing node is dropped from comparison but preserved.
func canonicalRotation(path []string) []string {
	if len(path) <= 1 {
		return append([]string(nil), path...)
	}
	ring := path[:len(path)-1] // drop repeated closing node
	minIdx := 0
	for i := 1; i < len(ring); i++ {
		if ring[i] < ring[minIdx] {
			minIdx = i
		}
	}
	out := make([]string, 0, len(ring)+1)
	for i := 0; i < len(ring); i++ {
		out = append(out, ring[(minIdx+i)%len(ring)])
	}
	out = append(out, ring[minIdx])
	return out
}

// Builder incrementally merges manifests into a graph.
type Builder struct {
	nodes map[string]*Node
	order []string
	roots map[string]bool
}

// NewBuilder creates an empty Builder.
func NewBuilder() *Builder {
	return &Builder{nodes: map[string]*Node{}, roots: map[string]bool{}}
}

// AddRoot marks a package name as a resolution root.
func (b *Builder) AddRoot(name string) {
	b.ensure(name, true)
	b.roots[name] = true
}

func (b *Builder) ensure(name string, root bool) *Node {
	n, ok := b.nodes[name]
	if !ok {
		n = &Node{Name: name}
		b.nodes[name] = n
		b.order = append(b.order, name)
	}
	if root {
		n.Root = true
	}
	return n
}

// AddManifest merges one manifest into the graph. Root manifests should be
// registered with AddRoot first when they represent workspace members or
// explicit roots; a manifest's package node is created regardless.
// Malformed requirement ranges fail with a distinct error carrying the
// declaration source.
func (b *Builder) AddManifest(m manifest.Manifest) error {
	from := b.ensure(m.Package, false)
	type key struct{ to string }
	index := map[string]*Edge{}
	for _, e := range from.Edges {
		index[e.To] = e
	}
	for _, dep := range m.Deps {
		if dep.Name == "" {
			return fmt.Errorf("manifest of %q declares a dependency with empty name", m.Package)
		}
		rng, err := semver.ParseRange(dep.Range)
		if err != nil {
			return &InvalidRangeError{
				Package: dep.Name,
				Range:   dep.Range,
				Source:  dep.Src,
				err:     err,
			}
		}
		b.ensure(dep.Name, false)
		e := index[dep.Name]
		if e == nil {
			e = &Edge{From: m.Package, To: dep.Name}
			from.Edges = append(from.Edges, e)
			index[dep.Name] = e
		}
		e.Decls = append(e.Decls, Declaration{
			Range:    rng,
			RawRange: dep.Range,
			Src:      dep.Src,
		})
	}
	sort.Slice(from.Edges, func(i, j int) bool { return from.Edges[i].To < from.Edges[j].To })
	return nil
}

// Build finalizes and returns the graph.
func (b *Builder) Build() *Graph {
	roots := make([]string, 0, len(b.roots))
	for r := range b.roots {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	order := make([]string, len(b.order))
	copy(order, b.order)
	sort.Strings(order)
	return &Graph{Roots: roots, nodes: b.nodes, order: order}
}

// InvalidRangeError marks a malformed version constraint in a manifest.
type InvalidRangeError struct {
	Package string
	Range   string
	Source  manifest.Source
	err     error
}

func (e *InvalidRangeError) Error() string {
	return fmt.Sprintf("invalid version range %q for %s (declared by %s): %v",
		e.Range, e.Package, sourceLabel(e.Source), e.err)
}

func (e *InvalidRangeError) Unwrap() error { return e.err }

func sourceLabel(s manifest.Source) string {
	switch {
	case s.Package_ != "":
		return s.Package_
	case s.Member != "":
		return "workspace:" + s.Member
	default:
		return s.Kind
	}
}

// BuildFromManifests starts from root manifests and lazily reads reachable
// package manifests through rd. Missing manifests are not fatal by
// themselves here: the solver reports them as missing packages against the
// registry. A manifest that is present but malformed is fatal. The graph
// merges every declaration, including repeated constraints on the same
// edge from different sources.
func BuildFromManifests(ctx context.Context, roots []manifest.Manifest, rd manifest.Reader) (*Graph, error) {
	b := NewBuilder()
	visited := map[string]bool{}
	queue := make([]manifest.Manifest, 0, len(roots))
	for _, m := range roots {
		b.AddRoot(m.Package)
		queue = append(queue, m)
	}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if visited[m.Package] {
			continue
		}
		visited[m.Package] = true
		if err := b.AddManifest(m); err != nil {
			return nil, err
		}
		// Deterministic traversal order.
		deps := append([]manifest.Requirement(nil), m.Deps...)
		sort.Slice(deps, func(i, j int) bool { return deps[i].Name < deps[j].Name })
		for _, dep := range deps {
			if visited[dep.Name] {
				continue
			}
			if rd == nil {
				continue
			}
			dm, err := rd.ReadManifest(ctx, dep.Name)
			if err != nil {
				if _, ok := err.(*manifest.ErrManifestNotFound); ok {
					b.ensure(dep.Name, false)
					continue
				}
				return nil, err
			}
			queue = append(queue, dm)
		}
	}
	return b.Build(), nil
}
