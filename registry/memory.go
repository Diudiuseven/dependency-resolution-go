package registry

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// MemoryRegistry is an in-memory Registry built from explicit releases; it
// is mainly used in tests and as the parsed form of a local index file.
type MemoryRegistry struct {
	mu       sync.RWMutex
	releases map[string][]Release // sorted ascending by version
}

// NewMemory builds a MemoryRegistry. Duplicate versions are rejected.
func NewMemory(releases []Release) (*MemoryRegistry, error) {
	r := &MemoryRegistry{releases: map[string][]Release{}}
	for _, rel := range releases {
		if err := r.Add(rel); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Add inserts one release keeping per-package slices sorted.
func (r *MemoryRegistry) Add(rel Release) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.releases[rel.Package]
	for _, ex := range list {
		if ex.Version.Equal(rel.Version) {
			return fmt.Errorf("duplicate release %s@%s", rel.Package, rel.Version)
		}
	}
	list = append(list, rel)
	sort.Slice(list, func(i, j int) bool { return list[i].Version.Less(list[j].Version) })
	r.releases[rel.Package] = list
	return nil
}

// Packages returns all known package names in sorted order.
func (r *MemoryRegistry) Packages() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.releases))
	for k := range r.releases {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (r *MemoryRegistry) Versions(ctx context.Context, pkg string) (semver.VersionList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	list, ok := r.releases[pkg]
	if !ok || len(list) == 0 {
		return nil, &ErrPackageMissing{Package: pkg}
	}
	out := make(semver.VersionList, len(list))
	for i, rel := range list {
		out[i] = rel.Version
	}
	return out, nil
}

func (r *MemoryRegistry) Release(ctx context.Context, pkg string, v semver.Version) (Release, error) {
	if err := ctx.Err(); err != nil {
		return Release{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rel := range r.releases[pkg] {
		if rel.Version.Equal(v) {
			return rel, nil
		}
	}
	return Release{}, &ErrVersionMissing{Package: pkg, Version: v.String()}
}

func (r *MemoryRegistry) Exists(ctx context.Context, pkg string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	list, ok := r.releases[pkg]
	return ok && len(list) > 0, nil
}
