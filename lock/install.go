package lock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// InstallItem is one artifact to install.
type InstallItem struct {
	Package string
	Version string
	Hash    string
}

// Plan is the deterministic install plan derived from a lock file.
type Plan struct {
	Items []InstallItem
}

// PlanInstall turns a lock file into an ordered install plan. Order is
// lexicographic by package then lane and never depends on map iteration;
// when the registry supplies dependency data, dependencies still sort
// deterministically (they are resolved from the lock units themselves).
func PlanInstall(ctx context.Context, f *File, reg registry.Registry) (*Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	units := append([]Unit(nil), f.Units...)
	sortUnits(units)
	p := &Plan{Items: make([]InstallItem, 0, len(units))}
	for _, u := range units {
		hash := u.Hash
		if hash == "" && reg != nil {
			v, err := semver.Parse(u.Version)
			if err != nil {
				return nil, fmt.Errorf("install %s: %w", u.Package, err)
			}
			if rel, err := reg.Release(ctx, u.Package, v); err == nil {
				hash = rel.Hash
			}
		}
		p.Items = append(p.Items, InstallItem{
			Package: u.Package, Version: u.Version, Hash: hash})
	}
	return p, nil
}

// InstallResult reports what was installed.
type InstallResult struct {
	Installed []InstallItem
	Skipped   []InstallItem // already present with matching hash
}

// Installer materializes artifacts into a local root directory. It only
// ever reads local registry content; there is no network.
type Installer struct {
	reg  registry.Registry
	root string
}

// NewInstaller creates an Installer writing under root.
func NewInstaller(reg registry.Registry, root string) *Installer {
	return &Installer{reg: reg, root: root}
}

// installedPath returns the on-disk location for an item.
func (in *Installer) installedPath(it InstallItem) string {
	return filepath.Join(in.root, filepath.FromSlash(it.Package), it.Version)
}

// Install executes the plan. Each item is written to a temporary directory
// and renamed into place; on context cancellation every item created by
// this invocation is rolled back, leaving no partial install state.
func (in *Installer) Install(ctx context.Context, p *Plan) (*InstallResult, error) {
	res := &InstallResult{}
	var created []string
	rollback := func() {
		for _, c := range created {
			_ = os.RemoveAll(c)
		}
	}
	for _, it := range p.Items {
		if err := ctx.Err(); err != nil {
			rollback()
			return nil, mapCanceled(err)
		}
		dest := in.installedPath(it)
		if in.matches(dest, it) {
			res.Skipped = append(res.Skipped, it)
			continue
		}
		if err := in.writeItem(ctx, it, dest, &created); err != nil {
			rollback()
			return nil, err
		}
		res.Installed = append(res.Installed, it)
	}
	return res, nil
}

func (in *Installer) writeItem(ctx context.Context, it InstallItem, dest string, created *[]string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dest), ".tmp-*")
	if err != nil {
		return err
	}
	*created = append(*created, tmp)
	// Materialize package metadata. Content is derived strictly from the
	// local registry release; the on-disk shape is one JSON file plus a
	// VERSION file, sufficient for offline verification.
	meta := []byte(fmt.Sprintf("package=%s\nversion=%s\nhash=%s\n", it.Package, it.Version, it.Hash))
	if err := os.WriteFile(filepath.Join(tmp, "PACKAGE.txt"), meta, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, "VERSION"), []byte(it.Version+"\n"), 0o644); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return mapCanceled(err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	// tmp renamed successfully: it is no longer a rollback candidate.
	*created = (*created)[:len(*created)-1]
	*created = append(*created, dest)
	return nil
}

func mapCanceled(err error) error {
	return fmt.Errorf("install canceled: %w", err)
}

// matches reports whether dest exists and carries the expected hash marker.
func (in *Installer) matches(dest string, it InstallItem) bool {
	data, err := os.ReadFile(filepath.Join(dest, "PACKAGE.txt"))
	if err != nil {
		return false
	}
	want := fmt.Sprintf("package=%s\nversion=%s\nhash=%s\n", it.Package, it.Version, it.Hash)
	return string(data) == want
}

// VerifyInstalled checks that every locked unit exists on disk with its
// recorded hash marker. Missing or changed items are returned as drift
// with install-specific wording.
func (in *Installer) VerifyInstalled(ctx context.Context, f *File) (*Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rep := &Report{LockHash: f.ID()}
	units := append([]Unit(nil), f.Units...)
	sortUnits(units)
	for _, u := range units {
		it := InstallItem{Package: u.Package, Version: u.Version, Hash: u.Hash}
		dest := in.installedPath(it)
		if !in.matches(dest, it) {
			rep.Drifts = append(rep.Drifts, Drift{
				Kind: DriftMissing, Package: u.Package,
				Lane: laneOfKey(u), Want: u.Version,
				Reason: "artifact absent or hash mismatch on disk",
			})
		}
	}
	sortDrifts(rep.Drifts)
	rep.OK = len(rep.Drifts) == 0
	return rep, nil
}
