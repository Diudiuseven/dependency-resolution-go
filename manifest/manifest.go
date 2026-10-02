// Package manifest defines dependency manifests and deterministic readers
// for them (JSON or a simple line based format).
package manifest

import "context"

// Source identifies where a declaration came from. It is carried through
// graph building and solving so every conflict can be explained.
type Source struct {
	Kind     string // "root" | "workspace" | "package"
	Member   string // workspace member path, empty for roots
	Package_ string // package whose manifest carries the requirement
	Range    string // raw constraint text at the declaration site
	File     string // manifest file path, when known
}

// Requirement is one declared dependency edge.
type Requirement struct {
	Name  string
	Range string
	Src   Source
}

// Manifest is one module's dependency declaration.
type Manifest struct {
	Package string
	Version string // empty for workspace members/roots
	File    string
	Deps    []Requirement
}

// Reader loads manifests by package name. Implementations must be safe for
// concurrent use. All data comes from local inputs only.
type Reader interface {
	ReadManifest(ctx context.Context, pkg string) (Manifest, error)
}

// ErrManifestNotFound is returned when no manifest exists for a package.
type ErrManifestNotFound struct{ Package string }

func (e *ErrManifestNotFound) Error() string { return "manifest not found: " + e.Package }
