package lock

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
	"github.com/Diudiuseven/dependency-resolution-go/solver"
)

func reg(t *testing.T) *registry.MemoryRegistry {
	r, err := registry.NewMemory([]registry.Release{
		{Package: "lib", Version: semver.MustParse("1.0.0"), Hash: "sha256:aaa",
			Deps: []registry.Dep{{Name: "util", Range: "^1.0.0"}}},
		{Package: "lib", Version: semver.MustParse("1.5.0"), Hash: "sha256:bbb",
			Deps: []registry.Dep{{Name: "util", Range: "^1.0.0"}}},
		{Package: "util", Version: semver.MustParse("1.0.0"), Hash: "sha256:ccc"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func solve(t *testing.T) *solver.Result {
	s := solver.New(reg(t), solver.DefaultOptions())
	m := manifest.Manifest{Package: "app"}
	m.Deps = []manifest.Requirement{{Name: "lib", Range: "^1.0.0",
		Src: manifest.Source{Kind: "root", Package_: "app", Range: "^1.0.0"}}}
	g, err := (&graphBuilder{}).with(m)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Solve(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestLockRoundTripCanonical(t *testing.T) {
	res := solve(t)
	f := FromResult(res)
	b1, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b2, err := f.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("lock serialization nondeterministic")
	}
	loaded, err := Unmarshal(b1)
	if err != nil {
		t.Fatal(err)
	}
	id1, id2 := f.ID(), loaded.ID()
	if id1 != id2 {
		t.Fatalf("id differs after round trip: %s vs %s", id1, id2)
	}
	if f.InputsHash == "" {
		t.Fatal("inputs hash missing")
	}
}

func TestSaveFileAtomic(t *testing.T) {
	res := solve(t)
	f := FromResult(res)
	path := filepath.Join(t.TempDir(), "nested", "dep.lock")
	if err := f.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID() != f.ID() {
		t.Fatal("loaded lock differs")
	}
}

func TestVerifyDetectsDrift(t *testing.T) {
	res := solve(t)
	f := FromResult(res)
	// Tamper: change lib version.
	for i := range f.Units {
		if f.Units[i].Package == "lib" {
			f.Units[i].Version = "1.0.0"
		}
	}
	rep, err := Verify(context.Background(), res, f)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || len(rep.Drifts) == 0 {
		t.Fatal("expected drift")
	}
	found := false
	for _, d := range rep.Drifts {
		if d.Kind == DriftChanged && d.Package == "lib" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing changed drift: %+v", rep.Drifts)
	}
}

func TestVerifyDetectsMissingAndExtra(t *testing.T) {
	res := solve(t)
	f := FromResult(res)
	// Drop util (missing) and inject ghost (extra).
	var units []Unit
	for _, u := range f.Units {
		if u.Package != "util" {
			units = append(units, u)
		}
	}
	units = append(units, Unit{Package: "ghost", Version: "9.9.9", Major: 9})
	f.Units = units
	rep, _ := Verify(context.Background(), res, f)
	kinds := map[DriftKind]bool{}
	for _, d := range rep.Drifts {
		kinds[d.Kind] = true
	}
	if !kinds[DriftMissing] || !kinds[DriftExtra] {
		t.Fatalf("want missing+extra, got %+v", rep.Drifts)
	}
}

func TestApplyPolicyFailAndRefresh(t *testing.T) {
	res := solve(t)
	f := FromResult(res)
	for i := range f.Units {
		if f.Units[i].Package == "lib" {
			f.Units[i].Version = "1.0.0"
		}
	}
	if _, _, err := ApplyPolicy(context.Background(), res, f, PolicyFail); err == nil {
		t.Fatal("PolicyFail must reject drift")
	}
	refreshed, drifts, err := ApplyPolicy(context.Background(), res, f, PolicyRefresh)
	if err != nil || len(drifts) == 0 {
		t.Fatalf("PolicyRefresh should report drift and fix: %v %d", err, len(drifts))
	}
	rep, _ := Verify(context.Background(), res, refreshed)
	if !rep.OK {
		t.Fatal("refreshed lock still drifts")
	}
}

func TestInstallAndVerify(t *testing.T) {
	res := solve(t)
	r := reg(t)
	f := FromResultWithHashes(res, r)
	plan, err := PlanInstall(context.Background(), f, r)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "modules")
	in := NewInstaller(r, root)
	out, err := in.Install(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Installed) == 0 {
		t.Fatal("nothing installed")
	}
	rep, err := in.VerifyInstalled(context.Background(), f)
	if err != nil || !rep.OK {
		t.Fatalf("verify installed failed: %v %+v", err, rep)
	}
	// Second install should skip everything.
	out2, err := in.Install(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(out2.Skipped) != len(plan.Items) {
		t.Fatalf("expected all skipped, got %d/%d", len(out2.Skipped), len(plan.Items))
	}
}
