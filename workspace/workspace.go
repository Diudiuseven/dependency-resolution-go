// Package workspace supports multi-member workspaces: each member holds its
// own manifest and members can reference one another. Resolution spans all
// members, while adding or removing an unrelated member never changes any
// other member's result.
package workspace

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// Member is one workspace component.
type Member struct {
	// Path is the workspace-relative unique identifier (e.g. "apps/web").
	Path string
	// PackageName is the package name the member provides locally.
	PackageName string
	// Manifest is the member's declared requirements.
	Manifest manifest.Manifest
}

// Workspace is a set of members resolved together.
type Workspace struct {
	mu      sync.RWMutex
	members map[string]Member // keyed by path
	byPkg   map[string]string // package name -> member path
}

// New builds a Workspace. Duplicate paths or package names are rejected.
func New(members []Member) (*Workspace, error) {
	w := &Workspace{members: map[string]Member{}, byPkg: map[string]string{}}
	// First pass: validate and register identity.
	for _, m := range members {
		if m.Path == "" {
			return nil, fmt.Errorf("workspace member has empty path")
		}
		if m.PackageName == "" {
			return nil, fmt.Errorf("workspace member %q has empty package name", m.Path)
		}
		if _, dup := w.members[m.Path]; dup {
			return nil, fmt.Errorf("duplicate workspace member path %q", m.Path)
		}
		if p, dup := w.byPkg[m.PackageName]; dup {
			return nil, fmt.Errorf("package %q provided by both members %q and %q",
				m.PackageName, p, m.Path)
		}
		if m.Manifest.Package == "" {
			m.Manifest.Package = m.PackageName
		}
		w.members[m.Path] = m
		w.byPkg[m.PackageName] = m.Path
	}
	// Second pass: annotate sources and rewrite member-to-member references
	// to the synthetic workspace version so they always resolve locally.
	for path, m := range w.members {
		for i := range m.Manifest.Deps {
			d := &m.Manifest.Deps[i]
			d.Src.Kind = "workspace"
			d.Src.Member = m.Path
			d.Src.Package_ = firstNonEmpty(d.Src.Package_, m.PackageName)
			if _, isMember := w.byPkg[d.Name]; isMember {
				d.Range = workspaceVersion.String()
				d.Src.Range = d.Range
			}
		}
		w.members[path] = m
	}
	return w, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Members returns members sorted by path for deterministic processing.
func (w *Workspace) Members() []Member {
	w.mu.RLock()
	defer w.mu.RUnlock()
	paths := make([]string, 0, len(w.members))
	for p := range w.members {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]Member, 0, len(paths))
	for _, p := range paths {
		out = append(out, w.members[p])
	}
	return out
}

// Member returns the member at a workspace path.
func (w *Workspace) Member(path string) (Member, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	m, ok := w.members[path]
	return m, ok
}

// MemberForPackage returns the member providing pkg, if any.
func (w *Workspace) MemberForPackage(pkg string) (Member, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	p, ok := w.byPkg[pkg]
	if !ok {
		return Member{}, false
	}
	return w.members[p], true
}

// RootManifests returns all member manifests in deterministic (path) order.
func (w *Workspace) RootManifests() []manifest.Manifest {
	ms := w.Members()
	out := make([]manifest.Manifest, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Manifest)
	}
	return out
}

// workspaceVersion is the synthetic version assigned to workspace members
// so they can participate in range checks without real releases.
var workspaceVersion = semver.MustParse("0.0.0-workspace")

// Registry wraps an upstream registry so workspace members shadow published
// releases: a workspace package resolves exactly to its member, never to a
// registry version. Constraints on workspace packages must be "*", "workspace:*",
// or absent; any other range yields a conflict (documented workspace semantics).
type Registry struct {
	ws  *Workspace
	up  registry.Registry
	ver semver.Version
}

// NewRegistry wraps ws and upstream with the synthetic member version v
// (defaults to 0.0.0-workspace when zero).
func NewRegistry(ws *Workspace, upstream registry.Registry, v semver.Version) *Registry {
	if v.Major == 0 && v.Minor == 0 && v.Patch == 0 && v.PreString() == "" {
		v = workspaceVersion
	}
	return &Registry{ws: ws, up: upstream, ver: v}
}

func (r *Registry) Versions(ctx context.Context, pkg string) (semver.VersionList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := r.ws.MemberForPackage(pkg); ok {
		return semver.VersionList{r.ver}, nil
	}
	if r.up == nil {
		return nil, &registry.ErrPackageMissing{Package: pkg}
	}
	return r.up.Versions(ctx, pkg)
}

func (r *Registry) Release(ctx context.Context, pkg string, v semver.Version) (registry.Release, error) {
	if m, ok := r.ws.MemberForPackage(pkg); ok {
		deps := make([]registry.Dep, 0, len(m.Manifest.Deps))
		for _, d := range m.Manifest.Deps {
			if _, isMember := r.ws.MemberForPackage(d.Name); isMember {
				// Workspace-to-workspace references always resolve to the
				// member regardless of the written range; normalize to "*".
				deps = append(deps, registry.Dep{Name: d.Name, Range: workspaceVersion.String()})
				continue
			}
			deps = append(deps, registry.Dep{Name: d.Name, Range: d.Range})
		}
		return registry.Release{Package: pkg, Version: r.ver,
			Hash: "workspace:" + m.Path, Deps: deps}, nil
	}
	if r.up == nil {
		return registry.Release{}, &registry.ErrVersionMissing{Package: pkg, Version: v.String()}
	}
	return r.up.Release(ctx, pkg, v)
}

func (r *Registry) Exists(ctx context.Context, pkg string) (bool, error) {
	if _, ok := r.ws.MemberForPackage(pkg); ok {
		return true, nil
	}
	if r.up == nil {
		return false, nil
	}
	return r.up.Exists(ctx, pkg)
}
