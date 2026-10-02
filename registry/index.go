package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// IndexFile is the JSON layout of a local registry snapshot:
//
//	{
//	  "packages": {
//	    "example.com/lib": [
//	      {"version":"1.2.3","hash":"sha256:...","deps":[{"name":"...","range":"^1.0.0"}]}
//	    ]
//	  }
//	}
type IndexFile struct {
	Packages map[string][]IndexRelease `json:"packages"`
}

// IndexRelease is one entry of IndexFile.
type IndexRelease struct {
	Version string     `json:"version"`
	Hash    string     `json:"hash,omitempty"`
	Deps    []IndexDep `json:"deps,omitempty"`
}

// IndexDep is a dependency entry inside an IndexRelease.
type IndexDep struct {
	Name  string `json:"name"`
	Range string `json:"range"`
}

// LoadIndex parses a local JSON index. All versions and ranges are
// validated up front so solving never encounters malformed catalogue data.
func LoadIndex(data []byte) (*MemoryRegistry, error) {
	var f IndexFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse registry index: %w", err)
	}
	var releases []Release
	names := make([]string, 0, len(f.Packages))
	for name := range f.Packages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("registry index contains empty package name")
		}
		for _, ir := range f.Packages[name] {
			v, err := semver.Parse(ir.Version)
			if err != nil {
				return nil, fmt.Errorf("registry index %s: %w", name, err)
			}
			deps := make([]Dep, 0, len(ir.Deps))
			for _, d := range ir.Deps {
				if strings.TrimSpace(d.Name) == "" {
					return nil, fmt.Errorf("registry index %s@%s: dependency with empty name", name, ir.Version)
				}
				if _, err := semver.ParseRange(d.Range); err != nil {
					return nil, fmt.Errorf("registry index %s@%s dependency %s: %w", name, ir.Version, d.Name, err)
				}
				deps = append(deps, Dep{Name: d.Name, Range: d.Range})
			}
			releases = append(releases, Release{
				Package: name,
				Version: v,
				Hash:    ir.Hash,
				Deps:    deps,
			})
		}
	}
	return NewMemory(releases)
}

// LoadIndexFile reads a local JSON index file.
func LoadIndexFile(path string) (*MemoryRegistry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return LoadIndex(data)
}

// DirRegistry treats a directory tree as a registry:
//
//	<dir>/<package>/<version>.json  -> {"hash":...,"deps":[...]}
//
// Package names with "/" map naturally to nested directories. Only local
// filesystem reads are performed.
type DirRegistry struct{ Root string }

type dirEntry struct {
	Hash string     `json:"hash,omitempty"`
	Deps []IndexDep `json:"deps,omitempty"`
}

func (r *DirRegistry) dirFor(pkg string) (string, error) {
	if pkg == "" {
		return "", fmt.Errorf("empty package name")
	}
	for _, part := range strings.Split(pkg, "/") {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid package name %q", pkg)
		}
	}
	dir := filepath.Join(r.Root, filepath.FromSlash(pkg))
	rel, err := filepath.Rel(r.Root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("package name %q escapes registry root", pkg)
	}
	return dir, nil
}

func (r *DirRegistry) Versions(ctx context.Context, pkg string) (semver.VersionList, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := r.dirFor(pkg)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &ErrPackageMissing{Package: pkg}
		}
		return nil, err
	}
	var out semver.VersionList
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		v, perr := semver.Parse(base)
		if perr != nil {
			return nil, fmt.Errorf("registry dir %s: %w", pkg, perr)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, &ErrPackageMissing{Package: pkg}
	}
	return out.Sorted(), nil
}

func (r *DirRegistry) Release(ctx context.Context, pkg string, v semver.Version) (Release, error) {
	if err := ctx.Err(); err != nil {
		return Release{}, err
	}
	dir, err := r.dirFor(pkg)
	if err != nil {
		return Release{}, err
	}
	path := filepath.Join(dir, v.Core()+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Release{}, &ErrVersionMissing{Package: pkg, Version: v.String()}
		}
		return Release{}, err
	}
	var de dirEntry
	if err := json.Unmarshal(data, &de); err != nil {
		return Release{}, fmt.Errorf("parse %s: %w", path, err)
	}
	deps := make([]Dep, 0, len(de.Deps))
	for _, d := range de.Deps {
		deps = append(deps, Dep{Name: d.Name, Range: d.Range})
	}
	return Release{Package: pkg, Version: v, Hash: de.Hash, Deps: deps}, nil
}

func (r *DirRegistry) Exists(ctx context.Context, pkg string) (bool, error) {
	vs, err := r.Versions(ctx, pkg)
	if err != nil {
		if _, ok := err.(*ErrPackageMissing); ok {
			return false, nil
		}
		return false, err
	}
	return len(vs) > 0, nil
}
