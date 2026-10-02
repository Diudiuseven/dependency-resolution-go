package resolve

import (
	"context"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
	"github.com/Diudiuseven/dependency-resolution-go/workspace"
)

func wsRel(pkg, ver string, deps ...registry.Dep) registry.Release {
	return registry.Release{Package: pkg, Version: semver.MustParse(ver), Deps: deps}
}

func memberManifest(pkg string, deps map[string]string) manifest.Manifest {
	m := manifest.Manifest{Package: pkg}
	for name, rng := range deps {
		m.Deps = append(m.Deps, manifest.Requirement{Name: name, Range: rng,
			Src: manifest.Source{Kind: "workspace", Package_: pkg, Range: rng}})
	}
	return m
}

func selMap(res interface{ Selections() }) {} // placeholder unused

func TestWorkspaceAcrossMembers(t *testing.T) {
	up, _ := registry.NewMemory([]registry.Release{
		wsRel("ext", "1.0.0"), wsRel("ext", "1.4.0"),
	})
	ws, err := workspace.New([]workspace.Member{
		{Path: "apps/one", PackageName: "example.com/one",
			Manifest: memberManifest("example.com/one", map[string]string{"ext": "^1.0.0"})},
		{Path: "apps/two", PackageName: "example.com/two",
			Manifest: memberManifest("example.com/two", map[string]string{
				"example.com/one": "*", "ext": "~1.4.0"})},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Config{Registry: workspace.NewRegistry(ws, up, semver.Version{})})
	f, res, err := r.FullRunWorkspace(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	sel := map[string]string{}
	for _, s := range res.Selections {
		sel[s.Package+"#"+s.Lane.Key()] = s.Version.String()
	}
	if sel["example.com/one#0.0"] != "0.0.0-workspace" {
		t.Fatalf("member not local: %v", sel)
	}
	// one is referenced by two, so it appears as a selected workspace unit;
	// two is only a root project (not depended on) and therefore need not be
	// selected as an installable unit.
	if sel["ext#1"] != "1.4.0" {
		t.Fatalf("ext=%s want 1.4.0", sel["ext#1"])
	}
	if f == nil || f.ID() == "" {
		t.Fatal("bad lock")
	}
}

func TestUnrelatedMemberStable(t *testing.T) {
	up, _ := registry.NewMemory([]registry.Release{
		wsRel("ext", "1.0.0"), wsRel("ext", "1.4.0"), wsRel("other", "2.0.0"),
	})
	solve := func(ms []workspace.Member) (map[string]string, string) {
		ws, err := workspace.New(ms)
		if err != nil {
			t.Fatal(err)
		}
		r := New(Config{Registry: workspace.NewRegistry(ws, up, semver.Version{})})
		f, res, err := r.FullRunWorkspace(context.Background(), ws)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		for _, s := range res.Selections {
			m[s.Package] = s.Version.String()
		}
		return m, f.ID()
	}
	base := []workspace.Member{
		{Path: "one", PackageName: "example.com/one",
			Manifest: memberManifest("example.com/one", map[string]string{"ext": "^1.0.0"})},
	}
	withTwo := append(append([]workspace.Member{}, base...), workspace.Member{
		Path: "two", PackageName: "example.com/two",
		Manifest: memberManifest("example.com/two", map[string]string{"other": "^2.0.0"})})
	m1, _ := solve(base)
	m2, _ := solve(withTwo)
	if m1["ext"] != m2["ext"] {
		t.Fatalf("ext drifted after adding unrelated member: %s vs %s", m1["ext"], m2["ext"])
	}
}

func TestWorkspaceDuplicateRejected(t *testing.T) {
	if _, err := workspace.New([]workspace.Member{
		{Path: "a", PackageName: "p"}, {Path: "a", PackageName: "q"},
	}); err == nil {
		t.Fatal("duplicate path accepted")
	}
	if _, err := workspace.New([]workspace.Member{
		{Path: "a", PackageName: "p"}, {Path: "b", PackageName: "p"},
	}); err == nil {
		t.Fatal("duplicate package accepted")
	}
}
