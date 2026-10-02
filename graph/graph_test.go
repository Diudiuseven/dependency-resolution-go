package graph

import (
	"context"
	"sort"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
)

func decl(from, to, rng string) manifest.Requirement {
	return manifest.Requirement{Name: to, Range: rng,
		Src: manifest.Source{Kind: "package", Package_: from, Range: rng}}
}

func TestMergeKeepsAllSources(t *testing.T) {
	b := NewBuilder()
	b.AddRoot("app")
	m1 := manifest.Manifest{Package: "app", Deps: []manifest.Requirement{
		decl("app", "lib", "^1.0.0"), decl("app", "lib2", "^2.0.0")}}
	m2 := manifest.Manifest{Package: "lib2", Deps: []manifest.Requirement{
		decl("lib2", "lib", "~1.4.0")}}
	if err := b.AddManifest(m1); err != nil {
		t.Fatal(err)
	}
	if err := b.AddManifest(m2); err != nil {
		t.Fatal(err)
	}
	g := b.Build()
	e, ok := g.Edge("app", "lib")
	if !ok {
		t.Fatal("missing app->lib")
	}
	if len(e.Decls) != 1 || e.Decls[0].RawRange != "^1.0.0" {
		t.Fatalf("app->lib decls=%+v", e.Decls)
	}
	all := g.EdgesTo("lib")
	if len(all) != 2 {
		t.Fatalf("lib should have 2 declaration sources, got %d", len(all))
	}
	// Deterministic ordering.
	if got := g.Nodes(); !sort.StringsAreSorted(got) {
		t.Fatalf("nodes not sorted: %v", got)
	}
}

func TestCycleDetection(t *testing.T) {
	b := NewBuilder()
	b.AddRoot("a")
	if err := b.AddManifest(manifest.Manifest{Package: "a",
		Deps: []manifest.Requirement{decl("a", "b", "^1")}}); err != nil {
		t.Fatal(err)
	}
	if err := b.AddManifest(manifest.Manifest{Package: "b",
		Deps: []manifest.Requirement{decl("b", "c", "^1")}}); err != nil {
		t.Fatal(err)
	}
	if err := b.AddManifest(manifest.Manifest{Package: "c",
		Deps: []manifest.Requirement{decl("c", "a", "^1")}}); err != nil {
		t.Fatal(err)
	}
	cycs := b.Build().Cycles()
	if len(cycs) != 1 {
		t.Fatalf("want 1 cycle, got %d", len(cycs))
	}
	// Canonical rotation starts at smallest node.
	if cycs[0].Path[0] != "a" {
		t.Fatalf("cycle not canonical: %v", cycs[0].Path)
	}
}

func TestInvalidRangeDistinctError(t *testing.T) {
	b := NewBuilder()
	b.AddRoot("app")
	err := b.AddManifest(manifest.Manifest{Package: "app",
		Deps: []manifest.Requirement{{Name: "lib", Range: ">>>1",
			Src: manifest.Source{Kind: "root", Package_: "app"}}}})
	if err == nil {
		t.Fatal("expected invalid range error")
	}
	if _, ok := err.(*InvalidRangeError); !ok {
		t.Fatalf("want *InvalidRangeError, got %T", err)
	}
}

func TestBuildFromManifestsMissingIsNodeNotCrash(t *testing.T) {
	rd := manifest.NewStaticReader(nil)
	roots := []manifest.Manifest{{Package: "app",
		Deps: []manifest.Requirement{decl("app", "ghost", "^1")}}}
	g, err := BuildFromManifests(context.Background(), roots, rd)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.Node("ghost"); !ok {
		t.Fatal("missing dep still becomes a node for the solver to report")
	}
}
