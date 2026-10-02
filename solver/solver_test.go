package solver

import (
	"context"
	"sort"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/compat"
	"github.com/Diudiuseven/dependency-resolution-go/graph"
	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

func mustRegistry(t *testing.T, rels []registry.Release) *registry.MemoryRegistry {
	t.Helper()
	r, err := registry.NewMemory(rels)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rel(pkg, ver string, deps ...registry.Dep) registry.Release {
	return registry.Release{Package: pkg, Version: semver.MustParse(ver), Deps: deps}
}

func rootManifest(pkg string, deps ...manifest.Requirement) manifest.Manifest {
	for i := range deps {
		deps[i].Src = manifest.Source{Kind: "root", Package_: pkg, Range: deps[i].Range}
	}
	return manifest.Manifest{Package: pkg, Deps: deps}
}

func dep(name, rng string) manifest.Requirement {
	return manifest.Requirement{Name: name, Range: rng}
}

func buildGraph(t *testing.T, roots []manifest.Manifest) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder()
	for i, m := range roots {
		if i == 0 {
			b.AddRoot(m.Package)
		}
		if err := b.AddManifest(m); err != nil {
			t.Fatal(err)
		}
	}
	return b.Build()
}

// regGraph builds a graph where the first manifest is the root and every
// release in reg contributes its dependency manifest (using the highest
// release's requirements as the package manifest is not correct, so callers
// relying on transitive deps must provide explicit manifests instead).
func regGraph(t *testing.T, root manifest.Manifest, extra ...manifest.Manifest) *graph.Graph {
	t.Helper()
	b := graph.NewBuilder()
	b.AddRoot(root.Package)
	if err := b.AddManifest(root); err != nil {
		t.Fatal(err)
	}
	for _, m := range extra {
		if err := b.AddManifest(m); err != nil {
			t.Fatal(err)
		}
	}
	return b.Build()
}

func selectedMap(res *Result) map[string]string {
	m := map[string]string{}
	for _, s := range res.Selections {
		m[s.Package+"#"+s.Lane.Key()] = s.Version.String()
	}
	return m
}

func TestSimpleHighest(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0"), rel("lib", "1.2.0"), rel("lib", "1.5.0"), rel("lib", "2.0.0"),
	})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app", dep("lib", "^1.0.0"))})
	res, err := s.Solve(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if got := selectedMap(res)["lib#1"]; got != "1.5.0" {
		t.Fatalf("got %s want 1.5.0", got)
	}
}

func TestMergedConstraintsFromMultipleSites(t *testing.T) {
	// app requires lib ^1.0; selected libA 1.1.0 requires lib ^1.2.
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0"), rel("lib", "1.1.0"), rel("lib", "1.2.0"), rel("lib", "1.9.0"),
		rel("a", "1.1.0", registry.Dep{Name: "lib", Range: "^1.2.0"}),
	})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app",
		dep("lib", "^1.0.0"), dep("a", "^1.1.0"))})
	res, err := s.Solve(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	sel := selectedMap(res)
	if sel["lib#1"] != "1.9.0" {
		t.Fatalf("lib=%s want 1.9.0", sel["lib#1"])
	}
	if sel["a#1"] != "1.1.0" {
		t.Fatalf("a=%s", sel["a#1"])
	}
}

func TestMultipleMajorsCoexist(t *testing.T) {
	// a -> lib ^1; b -> lib ^2. Both lanes coexist. Transitive deps of each
	// stay inside their own lane.
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0", registry.Dep{Name: "util", Range: "^1.0.0"}),
		rel("lib", "2.0.0", registry.Dep{Name: "util", Range: "^2.0.0"}),
		rel("util", "1.0.0"), rel("util", "2.0.0"),
		rel("a", "1.0.0", registry.Dep{Name: "lib", Range: "^1.0.0"}),
		rel("b", "1.0.0", registry.Dep{Name: "lib", Range: "^2.0.0"}),
	})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app",
		dep("a", "^1.0.0"), dep("b", "^1.0.0"))})
	res, err := s.Solve(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	sel := selectedMap(res)
	if sel["lib#1"] != "1.0.0" || sel["lib#2"] != "2.0.0" {
		t.Fatalf("lanes wrong: %v", sel)
	}
	if sel["util#1"] != "1.0.0" || sel["util#2"] != "2.0.0" {
		t.Fatalf("transitive lanes leaked across majors: %v", sel)
	}
}

func TestConflictReportedWithSources(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0"), rel("lib", "1.5.0"), rel("lib", "2.0.0"),
		rel("a", "1.0.0", registry.Dep{Name: "lib", Range: "~1.0.0"}),
		rel("b", "1.0.0", registry.Dep{Name: "lib", Range: ">=1.5.0 <2.0.0"}),
	})
	// a pulls lib ~1.0 (=>1.0,<1.1) and b pulls lib >=1.5<2 in the SAME
	// major-1 lane: intersection is empty.
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app",
		dep("a", "^1.0.0"), dep("b", "^1.0.0"))})
	_, err := s.Solve(context.Background(), g)
	f, ok := AsFailure(err)
	if !ok {
		t.Fatalf("expected failure, got %v", err)
	}
	if f.Kind != KindConflict {
		t.Fatalf("kind=%s want conflict: %v", f.Kind, err)
	}
	if len(f.Contradictions) == 0 || f.Contradictions[0].Package != "lib" {
		t.Fatalf("contradictions=%+v", f.Contradictions)
	}
	if len(f.Contradictions[0].Sources) < 2 {
		t.Fatalf("expected multiple sources, got %+v", f.Contradictions[0].Sources)
	}
}

func TestMissingPackage(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{rel("a", "1.0.0")})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app", dep("ghost", "^1.0.0"))})
	_, err := s.Solve(context.Background(), g)
	f, ok := AsFailure(err)
	if !ok || f.Kind != KindMissingPackage {
		t.Fatalf("got %v", err)
	}
}

func TestCycleDetected(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{
		rel("a", "1.0.0", registry.Dep{Name: "b", Range: "^1.0.0"}),
		rel("b", "1.0.0", registry.Dep{Name: "a", Range: "^1.0.0"}),
	})
	_ = reg
	s := New(reg, DefaultOptions())
	// Build a graph with a manifest-level cycle.
	m1 := rootManifest("a", dep("b", "^1.0.0"))
	m2 := manifest.Manifest{Package: "b", Deps: []manifest.Requirement{dep("a", "^1.0.0")}}
	for i := range m2.Deps {
		m2.Deps[i].Src = manifest.Source{Kind: "package", Package_: "b"}
	}
	b := graph.NewBuilder()
	b.AddRoot("a")
	if err := b.AddManifest(m1); err != nil {
		t.Fatal(err)
	}
	if err := b.AddManifest(m2); err != nil {
		t.Fatal(err)
	}
	_, err := s.Solve(context.Background(), b.Build())
	f, ok := AsFailure(err)
	if !ok || f.Kind != KindCycle {
		t.Fatalf("got %v", err)
	}
}

func TestDeterminismAcrossOrder(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0"), rel("lib", "1.9.0"),
		rel("a", "1.0.0", registry.Dep{Name: "lib", Range: ">=1.0.0 <2.0.0"}),
		rel("b", "1.0.0", registry.Dep{Name: "lib", Range: "^1.0.0"}),
	})
	s := New(reg, DefaultOptions())
	run := func(order []string) string {
		deps := []manifest.Requirement{}
		for _, n := range order {
			deps = append(deps, dep(n, "^1.0.0"))
		}
		g := buildGraph(t, []manifest.Manifest{rootManifest("app", deps...)})
		res, err := s.Solve(context.Background(), g)
		if err != nil {
			t.Fatal(err)
		}
		ks := make([]string, 0, len(res.Selections))
		for k, v := range selectedMap(res) {
			ks = append(ks, k+"="+v)
		}
		sort.Strings(ks)
		return res.InputsHash + "|" + joinStrings(ks)
	}
	h1 := run([]string{"a", "b"})
	h2 := run([]string{"b", "a"})
	if h1 != h2 {
		t.Fatalf("nondeterministic:\n%s\n%s", h1, h2)
	}
}

func TestBacktrackingFindsLowerVersion(t *testing.T) {
	// Within lane 1: lib 1.2.0 -> util ^2 (missing), lib 1.0.0 -> util ^1.
	// Pinned ^1 lane must backtrack from 1.2.0 down to 1.0.0.
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0", registry.Dep{Name: "util", Range: "^1.0.0"}),
		rel("lib", "1.2.0", registry.Dep{Name: "util", Range: "^2.0.0"}),
		rel("util", "1.0.0"),
	})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app", dep("lib", "^1.0.0"))})
	res, err := s.Solve(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if got := selectedMap(res)["lib#1"]; got != "1.0.0" {
		t.Fatalf("lib#1=%s want 1.0.0", got)
	}
	if got := selectedMap(res)["util#1"]; got != "1.0.0" {
		t.Fatalf("util#1=%s want 1.0.0", got)
	}
}

func TestOpenRangePicksHighestMajorDeterministically(t *testing.T) {
	// ">=1.0.0" deterministically takes major 2; if that whole lane is
	// infeasible the solve FAILS rather than silently downgrading majors.
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0", registry.Dep{Name: "util", Range: "^1.0.0"}),
		rel("lib", "2.0.0", registry.Dep{Name: "util", Range: "^2.0.0"}),
		rel("util", "1.0.0"),
	})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app", dep("lib", ">=1.0.0"))})
	_, err := s.Solve(context.Background(), g)
	if err == nil {
		t.Fatal("expected deterministic conflict, not silent major downgrade")
	}
	f, ok := AsFailure(err)
	if !ok {
		t.Fatal(err)
	}
	_ = f
}

func TestInvalidRangeFailsDistinctly(t *testing.T) {
	b := graph.NewBuilder()
	b.AddRoot("app")
	err := b.AddManifest(manifest.Manifest{Package: "app", Deps: []manifest.Requirement{{
		Name: "lib", Range: ">>1.0", Src: manifest.Source{Kind: "root", Package_: "app"},
	}}})
	if err == nil {
		t.Fatal("expected invalid range error")
	}
}

func joinStrings(ks []string) string {
	out := ""
	for i, k := range ks {
		if i > 0 {
			out += ","
		}
		out += k
	}
	return out
}

var _ = compat.DefaultConfig
