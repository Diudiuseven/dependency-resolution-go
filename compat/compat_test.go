package compat

import (
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

func TestStrictMajorLanes(t *testing.T) {
	cfg := DefaultConfig()
	vs := semver.VersionList{
		semver.MustParse("1.0.0"), semver.MustParse("1.5.0"),
		semver.MustParse("2.0.0"),
		semver.MustParse("0.2.1"), semver.MustParse("0.3.0"),
	}
	lanes := SplitMajors("lib", vs, cfg)
	keys := map[string]bool{}
	for _, l := range lanes {
		keys[l.Key()] = true
	}
	for _, k := range []string{"0.2", "0.3", "1", "2"} {
		if !keys[k] {
			t.Fatalf("missing lane %s in %v", k, keys)
		}
	}
	rule := cfg.RuleFor("lib").(StrictMajor)
	if rule.Allows(1, 0, 2, 0) {
		t.Fatal("major 1 must not be allowed in lane 2")
	}
	if !rule.Allows(1, 0, 1, 0) {
		t.Fatal("major 1 must be allowed in lane 1")
	}
	if rule.Allows(0, 2, 0, 3) {
		t.Fatal("0.2 and 0.3 are breaking-incompatible")
	}
	if msg := rule.Explain(1, 0, 2, 0, false); msg == "" {
		t.Fatal("explain should give a reason")
	}
}

func TestSameMajorSharesZero(t *testing.T) {
	cfg := Config{Rule: SameMajor{}}
	vs := semver.VersionList{semver.MustParse("0.2.1"), semver.MustParse("0.3.0")}
	lanes := SplitMajors("lib", vs, cfg)
	if len(lanes) != 1 || lanes[0].Key() != "0" {
		t.Fatalf("SameMajor should merge 0.x into one lane, got %v", lanes)
	}
}

func TestPerPackageOverride(t *testing.T) {
	cfg := DefaultConfig()
	cfg.PerPackage = map[string]Rule{"lib": SameMajor{}}
	if _, ok := cfg.RuleFor("lib").(SameMajor); !ok {
		t.Fatal("per-package override not applied")
	}
	if _, ok := cfg.RuleFor("other").(StrictMajor); !ok {
		t.Fatal("default rule not used for non-overridden package")
	}
}
