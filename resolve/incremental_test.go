package resolve

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/lock"
	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
)

// incrementalFixture: lib (1.x/2.x), a (->lib ^1), b (->lib ^2), stable
// leaf dep "stub" that must never change when unrelated manifests change.
func incrementalRegistry(t *testing.T) *registry.MemoryRegistry {
	return registryWith(t,
		mkRelease("lib", "1.0.0"), mkRelease("lib", "1.5.0"),
		mkRelease("lib", "2.0.0"),
		mkRelease("a", "1.0.0", registry.Dep{Name: "lib", Range: "^1.0.0"}),
		mkRelease("b", "1.0.0", registry.Dep{Name: "lib", Range: "^2.0.0"}),
		mkRelease("stub", "1.0.0"), mkRelease("stub", "1.1.0"),
		mkRelease("churn", "1.0.0", registry.Dep{Name: "stub", Range: "^1.0.0"}),
		mkRelease("churn", "1.1.0", registry.Dep{Name: "stub", Range: "~1.0.0"}),
	)
}

func TestIncrementalEqualsFullAndStability(t *testing.T) {
	reg := incrementalRegistry(t)
	dir := t.TempDir()
	r := New(Config{Registry: reg, LockPath: filepath.Join(dir, "dep.lock")})
	ctx := context.Background()

	roots1 := []manifest.Manifest{root("app", map[string]string{
		"a": "^1.0.0", "b": "^1.0.0", "churn": "^1.0.0"})}
	f1, res1, err := r.FullRun(ctx, roots1)
	if err != nil {
		t.Fatal(err)
	}
	snap := &Snapshot{Roots: roots1, Result: res1, Lock: f1}

	// Change churn's constraint indirectly by switching requested range.
	roots2 := []manifest.Manifest{root("app", map[string]string{
		"a": "^1.0.0", "b": "^1.0.0", "churn": "~1.0.0"})}
	f2, res2, changed, err := r.IncrementalRun(ctx, snap, roots2,
		[]Change{{Kind: ChangeModified, Package: "app"}})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected a changed flag")
	}
	// Incremental result must equal a fresh full recomputation.
	fFull, resFull, err := New(Config{Registry: reg}).FullRun(ctx, roots2)
	if err != nil {
		t.Fatal(err)
	}
	if f2.ID() != fFull.ID() {
		t.Fatalf("incremental lock %s != full lock %s", f2.ID(), fFull.ID())
	}
	// Unaffected coexisting majors of lib keep identical versions.
	want := map[string]string{}
	for _, s := range resFull.Selections {
		want[s.Package+"#"+s.Lane.Key()] = s.Version.String()
	}
	for _, s := range res2.Selections {
		k := s.Package + "#" + s.Lane.Key()
		if want[k] != s.Version.String() {
			t.Fatalf("incremental/full mismatch on %s", k)
		}
	}
}

func TestIncrementalUnrelatedChangeKeepsLock(t *testing.T) {
	reg := incrementalRegistry(t)
	r := New(Config{Registry: reg})
	ctx := context.Background()
	roots1 := []manifest.Manifest{root("app", map[string]string{
		"a": "^1.0.0", "b": "^1.0.0"})}
	_, res1, err := r.FullRun(ctx, roots1)
	if err != nil {
		t.Fatal(err)
	}
	// Add a brand-new unrelated member dependency.
	roots2 := []manifest.Manifest{root("app", map[string]string{
		"a": "^1.0.0", "b": "^1.0.0", "stub": "^1.0.0"})}
	snap := &Snapshot{Roots: roots1, Result: res1}
	f2, res2, changed, err := r.IncrementalRun(ctx, snap, roots2,
		[]Change{{Kind: ChangeAdded, Package: "stub"}})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("adding stub should report a change in unit set")
	}
	// Existing a,b,lib#1,lib#2 versions must be byte-identical.
	old := map[string]string{}
	for _, s := range res1.Selections {
		old[s.Package+"#"+s.Lane.Key()] = s.Version.String()
	}
	for _, s := range res2.Selections {
		k := s.Package + "#" + s.Lane.Key()
		if v, existed := old[k]; existed && v != s.Version.String() {
			t.Fatalf("unrelated add changed %s: %s -> %s", k, v, s.Version)
		}
	}
	if f2 == nil {
		t.Fatal("nil incremental lock")
	}
}

func TestIncrementalCanceledLeavesLock(t *testing.T) {
	reg := incrementalRegistry(t)
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "dep.lock")
	r := New(Config{Registry: reg, LockPath: lockPath})
	ctx := context.Background()
	roots1 := []manifest.Manifest{root("app", map[string]string{"a": "^1.0.0"})}
	f1, res1, err := r.FullRun(ctx, roots1)
	if err != nil {
		t.Fatal(err)
	}
	before, err := lock.LoadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	roots2 := []manifest.Manifest{root("app", map[string]string{"a": "^1.0.0", "b": "^1.0.0"})}
	_, _, _, _ = r.IncrementalRun(cctx, &Snapshot{Roots: roots1, Result: res1, Lock: f1},
		roots2, []Change{{Kind: ChangeAdded, Package: "b"}})
	after, err := lock.LoadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if before.ID() != after.ID() {
		t.Fatal("canceled incremental run modified the lock")
	}
}

func TestMultiMajorCoexistEndToEnd(t *testing.T) {
	reg := incrementalRegistry(t)
	r := New(Config{Registry: reg})
	ctx := context.Background()
	roots := []manifest.Manifest{root("app", map[string]string{
		"a": "^1.0.0", "b": "^1.0.0"})}
	_, res, err := r.FullRun(ctx, roots)
	if err != nil {
		t.Fatal(err)
	}
	lanes := map[string]bool{}
	versions := map[string]string{}
	for _, s := range res.Selections {
		if s.Package == "lib" {
			lanes[s.Lane.Key()] = true
			versions[s.Lane.Key()] = s.Version.String()
		}
	}
	if !lanes["1"] || !lanes["2"] {
		t.Fatalf("lib majors 1 and 2 must coexist: %v", versions)
	}
	if versions["1"] != "1.5.0" || versions["2"] != "2.0.0" {
		t.Fatalf("wrong coexisting versions: %v", versions)
	}
	// Explainability: each lib selection carries reasons from its lane only.
	for _, s := range res.Selections {
		if s.Package != "lib" {
			continue
		}
		if len(s.Reasons) == 0 {
			t.Fatal("selection without reason sources")
		}
	}
}
