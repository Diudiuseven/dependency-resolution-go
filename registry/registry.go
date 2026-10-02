// Package registry provides the local-only catalogue of available package
// versions and their dependency manifests. No network access is ever used.
package registry

import (
	"context"

	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// Release describes one published version of a package.
type Release struct {
	Package string
	Version semver.Version
	Hash    string // content hash, e.g. "sha256:..."
	// Deps are the raw dependency constraints declared by this release.
	Deps []Dep
}

// Dep is a constraint as declared inside a published release.
type Dep struct {
	Name  string
	Range string
}

// Registry lists locally available releases. Implementations must be safe
// for concurrent use and return deterministically ordered results.
type Registry interface {
	// Versions returns all locally known versions of pkg, sorted ascending.
	Versions(ctx context.Context, pkg string) (semver.VersionList, error)
	// Release returns the release record for a specific package version.
	Release(ctx context.Context, pkg string, v semver.Version) (Release, error)
	// Exists reports whether a package is known at all.
	Exists(ctx context.Context, pkg string) (bool, error)
}

// ErrPackageMissing indicates a package that has no local releases.
type ErrPackageMissing struct{ Package string }

func (e *ErrPackageMissing) Error() string { return "package not available locally: " + e.Package }

// ErrVersionMissing indicates a package exists but the requested version is
// not available locally.
type ErrVersionMissing struct {
	Package string
	Version string
}

func (e *ErrVersionMissing) Error() string {
	return "version not available locally: " + e.Package + "@" + e.Version
}
