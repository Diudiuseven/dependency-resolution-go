package resolve

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/lock"
	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
	"github.com/Diudiuseven/dependency-resolution-go/solver"
)

func mkRelease(pkg, ver string, deps ...registry.Dep) registry.Release {
	return registry.Release{Package: pkg, Version: semver.MustParse(ver), Deps: deps}
}

func root(pkg string, deps map[string]string) manifest.Manifest {
	m := manifest.Manifest{Package: pkg}
	for name, rng := range deps {
		m.Deps = append(m.Deps, manifest.Requirement{
			Name: name, Range: rng,
			Src: manifest.Source{Kind: "root", Package_: pkg, Range: rng},
		})
	}
	return m
}

// registryWith builds a memory registry from a compact spec.
func registryWith(t *testing.T, rels ...registry.Release) *registry.MemoryRegistry {
	t.Helper()
	r, err := registry.NewMemory(rels)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFullResolveEndToEnd(t *testing.T) {
	reg := registryWith(t,
		mkRelease("example.com/a", "1.0.0", registry.Dep{Name: "example.com/lib", Range: "^1.0.0"}),
		mkRelease("example.com/a", "1.2.0", registry.Dep{Name: "example.com/lib", Range: "^1.5.0"}),
		mkRelease("example.com/lib", "1.0.0"),
		mkRelease("example.com/lib", "1.5.0"),
		mkRelease("example.com/lib", "1.9.0"),
	)
	dir := t.TempDir()
	r := New(Config{Registry: reg, LockPath: filepath.Join(dir, "dep.lock")})
	m := root("app", map[string]string{"example.com/a": "^1.0.0"})
	f, res, err := r.FullRun(context.Background(), []manifest.Manifest{m})
	if err != nil {
		t.Fatal(err)
	}
	sel := map[string]string{}
	for _, s := range res.Selections {
		sel[s.Package+"#"+s.Lane.Key()] = s.Version.String()
	}
	if sel["example.com/a#1"] != "1.2.0" {
		t.Fatalf("a=%s", sel["example.com/a#1"])
	}
	if sel["example.com/lib#1"] != "1.9.0" {
		t.Fatalf("lib=%s", sel["example.com/lib#1"])
	}
	// Lock must reload and verify.
	loaded, err := lock.LoadFile(filepath.Join(dir, "dep.lock"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := lock.Verify(context.Background(), res, loaded)
	if err != nil || !rep.OK {
		t.Fatalf("verify failed: %v %+v", err, rep)
	}
	if f.ID() != loaded.ID() {
		t.Fatal("lock id unstable across load")
	}
}

func TestDeterminismAcrossRepeatedRuns(t *testing.T) {
	reg := registryWith(t,
		mkRelease("lib", "1.0.0"), mkRelease("lib", "1.9.0"), mkRelease("lib", "2.0.0"),
		mkRelease("a", "1.0.0", registry.Dep{Name: "lib", Range: "^1.0.0"}),
		mkRelease("b", "1.0.0", registry.Dep{Name: "lib", Range: "1.x"}),
	)
	r := New(Config{Registry: reg})
	var ids []string
	for i := 0; i < 5; i++ {
		m := root("app", map[string]string{"a": "^1.0.0", "b": "^1.0.0"})
		f, _, err := r.FullRun(context.Background(), []manifest.Manifest{m})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, f.ID())
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] != ids[0] {
			t.Fatalf("nondeterministic lock ids: %v", ids)
		}
	}
}

func TestConflictEndToEnd(t *testing.T) {
	reg := registryWith(t,
		mkRelease("lib", "1.0.0"), mkRelease("lib", "1.9.0"),
		mkRelease("a", "1.0.0", registry.Dep{Name: "lib", Range: "~1.0.0"}),
		mkRelease("b", "1.0.0", registry.Dep{Name: "lib", Range: ">=1.5.0"}),
	)
	r := New(Config{Registry: reg})
	_, _, err := r.FullRun(context.Background(),
		[]manifest.Manifest{root("app", map[string]string{"a": "^1.0.0", "b": "^1.0.0"})})
	f, ok := solver.AsFailure(err)
	if !ok || f.Kind != solver.KindConflict {
		t.Fatalf("want conflict, got %v", err)
	}
	if len(f.Contradictions[0].Sources) < 2 {
		t.Fatalf("conflict must name multiple sources: %+v", f.Contradictions)
	}
}

func TestMissingPackageEndToEnd(t *testing.T) {
	reg := registryWith(t, mkRelease("a", "1.0.0"))
	r := New(Config{Registry: reg})
	_, _, err := r.FullRun(context.Background(),
		[]manifest.Manifest{root("app", map[string]string{"ghost": "^1.0.0"})})
	f, ok := solver.AsFailure(err)
	if !ok || f.Kind != solver.KindMissingPackage {
		t.Fatalf("want missing_package, got %v", err)
	}
}

func TestInvalidRangeEndToEnd(t *testing.T) {
	reg := registryWith(t, mkRelease("a", "1.0.0"))
	r := New(Config{Registry: reg})
	_, _, err := r.FullRun(context.Background(),
		[]manifest.Manifest{root("app", map[string]string{"a": "not-a-range"})})
	f, ok := solver.AsFailure(err)
	if !ok || f.Kind != solver.KindInvalidRange {
		t.Fatalf("want invalid_range, got %v", err)
	}
}

func TestCancelationLeavesNoLock(t *testing.T) {
	reg := registryWith(t, mkRelease("a", "1.0.0"))
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "dep.lock")
	r := New(Config{Registry: reg, LockPath: lockPath})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	_, _, _ = r.FullRun(ctx, []manifest.Manifest{root("app", map[string]string{"a": "^1.0.0"})})
	if _, err := lock.LoadFile(lockPath); err == nil {
		t.Fatal("no lock file may exist after a canceled resolve")
	}
}
