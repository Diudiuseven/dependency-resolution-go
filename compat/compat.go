// Package compat defines configurable major-version compatibility rules:
// how dependency declarations are grouped into "lanes" (independently
// coexisting major versions) and how transitive requirements may span them.
package compat

import (
	"fmt"
	"sort"

	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// Rule decides whether a declaration with concrete major dMajor (and, for
// major 0 under StrictMajor, breaking minor dMinor) is allowed to land in
// an existing lane identified by (laneMajor, laneMinor where relevant).
type Rule interface {
	Allows(dMajor, dMinor, laneMajor, laneMinor uint64) bool
	// Explain returns a human-readable reason for an Allows decision.
	Explain(dMajor, dMinor, laneMajor, laneMinor uint64, allowed bool) string
	// LaneKey renders the lane identity used in results and lock files.
	LaneKey(major, minor uint64) string
}

// StrictMajor implements semver-style major isolation: declarations only
// ever land on their own exact major lane; major 0 treats each minor as
// incompatible (0.x.y <-> 0.z.w incompatible when x != z).
type StrictMajor struct {
	// TreatZeroMajorAsBreaking makes every 0.minor its own lane.
	TreatZeroMajorAsBreaking bool
}

func (r StrictMajor) Allows(dMajor, dMinor, laneMajor, laneMinor uint64) bool {
	if dMajor != laneMajor {
		return false
	}
	if r.TreatZeroMajorAsBreaking && dMajor == 0 && dMinor != laneMinor {
		return false
	}
	return true
}

func (r StrictMajor) Explain(dMajor, dMinor, laneMajor, laneMinor uint64, allowed bool) string {
	if allowed {
		if r.TreatZeroMajorAsBreaking && dMajor == 0 {
			return fmt.Sprintf("declaration targets 0.%d.x and lane is 0.%d.x", dMinor, laneMinor)
		}
		return fmt.Sprintf("declaration major %d equals lane major %d", dMajor, laneMajor)
	}
	switch {
	case dMajor != laneMajor:
		return fmt.Sprintf("declaration requires major %d but lane is major %d (different major versions never satisfy one another)", dMajor, laneMajor)
	case r.TreatZeroMajorAsBreaking && dMajor == 0:
		return fmt.Sprintf("declaration requires 0.%d.x but lane is 0.%d.x (every 0.minor is breaking)", dMinor, laneMinor)
	default:
		return "incompatible lanes"
	}
}

func (r StrictMajor) LaneKey(major, minor uint64) string {
	if r.TreatZeroMajorAsBreaking && major == 0 {
		return fmt.Sprintf("0.%d", minor)
	}
	return fmt.Sprintf("%d", major)
}

// SameMajor allows declarations with the same major only; all 0.x share a
// single lane.
type SameMajor struct{}

func (r SameMajor) Allows(dMajor, dMinor, laneMajor, laneMinor uint64) bool {
	return dMajor == laneMajor
}

func (r SameMajor) Explain(dMajor, dMinor, laneMajor, laneMinor uint64, allowed bool) string {
	if allowed {
		return fmt.Sprintf("declaration major %d equals lane major %d", dMajor, laneMajor)
	}
	return fmt.Sprintf("declaration requires major %d but lane is major %d", dMajor, laneMajor)
}

func (r SameMajor) LaneKey(major, minor uint64) string { return fmt.Sprintf("%d", major) }

// Config configures lane construction.
type Config struct {
	Rule Rule
	// PerPackage overrides the default rule for specific package names.
	PerPackage map[string]Rule
}

// DefaultConfig returns the recommended configuration (StrictMajor with
// 0.minor treated as breaking).
func DefaultConfig() Config {
	return Config{Rule: StrictMajor{TreatZeroMajorAsBreaking: true}}
}

// RuleFor resolves the effective rule for a package.
func (c Config) RuleFor(pkg string) Rule {
	if r, ok := c.PerPackage[pkg]; ok {
		return r
	}
	if c.Rule == nil {
		return StrictMajor{TreatZeroMajorAsBreaking: true}
	}
	return c.Rule
}

// Lane is one coexisting major-version instance of a package.
type Lane struct {
	Package string
	Major   uint64
	// Minor carries the breaking minor for major==0 under StrictMajor;
	// zero otherwise.
	Minor uint64
	key   string
}

// Key is the stable identity of the lane, e.g. "2" or "0.4".
func (l Lane) Key() string {
	if l.key != "" {
		return l.key
	}
	if l.Major == 0 && l.Minor != 0 {
		return fmt.Sprintf("0.%d", l.Minor)
	}
	return fmt.Sprintf("%d", l.Major)
}

func (l Lane) String() string { return l.Package + "#" + l.Key() }

// NewLane constructs a lane with an explicit key under rule.
func NewLane(pkg string, major, minor uint64, rule Rule) Lane {
	return Lane{Package: pkg, Major: major, Minor: minor, key: rule.LaneKey(major, minor)}
}

// LaneOf returns the lane a concrete version belongs to under cfg.
func LaneOf(pkg string, v semver.Version, cfg Config) Lane {
	rule := cfg.RuleFor(pkg)
	var minor uint64
	if sm, ok := rule.(StrictMajor); ok && sm.TreatZeroMajorAsBreaking && v.Major == 0 {
		minor = v.Minor
	}
	return Lane{Package: pkg, Major: v.Major, Minor: minor, key: rule.LaneKey(v.Major, minor)}
}

// SplitMajors partitions available versions into lanes per the config.
// Lanes are returned in ascending key order deterministically.
func SplitMajors(pkg string, versions semver.VersionList, cfg Config) []Lane {
	byKey := map[string]Lane{}
	var keys []string
	for _, v := range versions {
		l := LaneOf(pkg, v, cfg)
		if _, ok := byKey[l.Key()]; !ok {
			byKey[l.Key()] = l
			keys = append(keys, l.Key())
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := byKey[keys[i]], byKey[keys[j]]
		if a.Major != b.Major {
			return a.Major < b.Major
		}
		return a.Minor < b.Minor
	})
	out := make([]Lane, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}
