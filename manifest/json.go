package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File is the on-disk JSON shape of a manifest.
type File struct {
	Package    string            `json:"package"`
	Version    string            `json:"version,omitempty"`
	Requires   map[string]string `json:"requires,omitempty"`
	SourceFile string            `json:"-"`
}

// FromFile parses manifest JSON bytes. srcFile is recorded for diagnostics.
// Requirements are normalized: dependency names sorted, and every
// requirement carries the declaration source.
func FromFile(data []byte, srcFile string) (Manifest, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest %s: %w", srcFile, err)
	}
	if strings.TrimSpace(f.Package) == "" {
		return Manifest{}, fmt.Errorf("manifest %s is missing a package name", srcFile)
	}
	m := Manifest{
		Package: f.Package,
		Version: f.Version,
		File:    srcFile,
		Deps:    make([]Requirement, 0, len(f.Requires)),
	}
	names := make([]string, 0, len(f.Requires))
	for name := range f.Requires {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		rng := strings.TrimSpace(f.Requires[name])
		m.Deps = append(m.Deps, Requirement{
			Name:  name,
			Range: rng,
			Src: Source{
				Kind:     "package",
				Package_: f.Package,
				Range:    rng,
				File:     srcFile,
			},
		})
	}
	return m, nil
}

// LoadFile reads and parses a manifest file from the local filesystem.
func LoadFile(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	return FromFile(data, path)
}

// StaticReader serves a fixed set of manifests from memory. Lookup is by
// package name and never iterates maps in caller-visible order.
type StaticReader struct {
	items map[string]Manifest
}

// NewStaticReader builds a StaticReader from manifests.
func NewStaticReader(ms []Manifest) *StaticReader {
	r := &StaticReader{items: make(map[string]Manifest, len(ms))}
	for _, m := range ms {
		r.items[m.Package] = m
	}
	return r
}

// Add inserts or replaces one manifest (mainly used by workspace setup).
func (r *StaticReader) Add(m Manifest) { r.items[m.Package] = m }

// Packages returns the served package names in sorted order.
func (r *StaticReader) Packages() []string {
	out := make([]string, 0, len(r.items))
	for k := range r.items {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *StaticReader) ReadManifest(ctx context.Context, pkg string) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if m, ok := r.items[pkg]; ok {
		return m, nil
	}
	return Manifest{}, &ErrManifestNotFound{Package: pkg}
}

// DirReader reads manifests named "<dir>/<pkg>.json" from a local directory.
// Package names with "/" map to nested paths; reads never leave Dir.
type DirReader struct{ Dir string }

func (r *DirReader) ReadManifest(ctx context.Context, pkg string) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if !safeName(pkg) {
		return Manifest{}, fmt.Errorf("invalid package name %q", pkg)
	}
	path := filepath.Join(r.Dir, filepath.FromSlash(pkg)+".json")
	rel, err := filepath.Rel(r.Dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Manifest{}, fmt.Errorf("package name %q escapes manifest directory", pkg)
	}
	return LoadFile(path)
}

// safeName rejects empty names and names containing "..".
func safeName(pkg string) bool {
	if pkg == "" {
		return false
	}
	for _, part := range strings.Split(pkg, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
