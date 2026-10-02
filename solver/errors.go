package solver

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Diudiuseven/dependency-resolution-go/graph"
	"github.com/Diudiuseven/dependency-resolution-go/manifest"
)

// FailureKind enumerates the distinguishable failure reasons.
type FailureKind string

const (
	KindConflict       FailureKind = "conflict"        // constraints cannot be jointly satisfied
	KindCycle          FailureKind = "cycle"           // dependency graph contains a cycle
	KindMissingPackage FailureKind = "missing_package" // required package has no local versions
	KindMissingVersion FailureKind = "missing_version" // lock/install asks for an unavailable version
	KindInvalidRange   FailureKind = "invalid_range"   // malformed version constraint
	KindInvalidVersion FailureKind = "invalid_version" // malformed version
	KindCanceled       FailureKind = "canceled"        // context canceled / deadline exceeded
	KindExhausted      FailureKind = "exhausted"       // search budget exhausted
)

// Contradiction ties one unsatisfiable constraint intersection to all the
// declaration sites that produced it.
type Contradiction struct {
	Package string
	Lane    string
	Range   string // the intersection that no available version satisfies
	Sources []manifest.Source
}

func (c Contradiction) String() string {
	srcs := make([]string, 0, len(c.Sources))
	for _, s := range c.Sources {
		srcs = append(srcs, describeSource(s))
	}
	sort.Strings(srcs)
	return fmt.Sprintf("%s#%s: no available version satisfies [%s]; declared by %s",
		c.Package, c.Lane, c.Range, strings.Join(srcs, ", "))
}

// Failure is the structured, human-readable result of an unsuccessful solve.
type Failure struct {
	Kind           FailureKind
	Message        string
	Contradictions []Contradiction
	Cycle          []string
	Package        string
	Version        string
	wrapped        error
}

func (e *Failure) Error() string {
	if e.Message != "" {
		return string(e.Kind) + ": " + e.Message
	}
	return string(e.Kind)
}

func (e *Failure) Unwrap() error { return e.wrapped }

// AsFailure extracts a *Failure from err if present.
func AsFailure(err error) (*Failure, bool) {
	var f *Failure
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

func describeSource(s manifest.Source) string {
	switch {
	case s.Member != "" && s.Package_ != "":
		return fmt.Sprintf("%s (member %s, constraint %q)", s.Package_, s.Member, s.Range)
	case s.Member != "":
		return fmt.Sprintf("workspace member %s (constraint %q)", s.Member, s.Range)
	case s.Package_ != "":
		return fmt.Sprintf("%s (constraint %q)", s.Package_, s.Range)
	default:
		return fmt.Sprintf("%s (constraint %q)", s.Kind, s.Range)
	}
}

func canceledFailure(err error) *Failure {
	return &Failure{Kind: KindCanceled, Message: "resolution canceled: " + err.Error(), wrapped: err}
}

// cycleFailureFromPath builds a KindCycle failure from an in-progress DFS
// path, used when a transitive (release-declared) edge closes a cycle.
func cycleFailureFromPath(path []string) *Failure {
	c := graph.Cycle{Path: append(append([]string{}, path...), path[0])}
	return cycleFailure([]graph.Cycle{c})
}

func cycleFailure(cycles []graph.Cycle) *Failure {
	f := &Failure{Kind: KindCycle,
		Message: fmt.Sprintf("dependency cycle detected: %s", cycles[0].String())}
	for _, c := range cycles {
		f.Cycle = append(f.Cycle, c.Path...)
		f.Message += "\n  cycle: " + c.String()
	}
	return f
}

func missingPackageFailure(pkg string, decls []graph.Declaration) *Failure {
	srcs := make([]manifest.Source, 0, len(decls))
	for _, d := range decls {
		srcs = append(srcs, d.Src)
	}
	labels := make([]string, 0, len(srcs))
	for _, s := range srcs {
		labels = append(labels, describeSource(s))
	}
	sort.Strings(labels)
	return &Failure{
		Kind:    KindMissingPackage,
		Package: pkg,
		Message: fmt.Sprintf("no local versions available for %s, required by %s",
			pkg, strings.Join(labels, "; ")),
	}
}

// intersectionFailureUpTo builds a conflict from a lane considering only
// declarations introduced at depth <= maxDepth.
func intersectionFailureUpTo(st *laneState, maxDepth int) *Failure {
	decls := st.decls
	c := Contradiction{Package: st.lane.Package, Lane: st.lane.Key(),
		Range: intersectionString(decls)}
	seen := map[string]bool{}
	for _, d := range decls {
		label := describeSource(d.Src)
		if !seen[label] {
			seen[label] = true
			c.Sources = append(c.Sources, d.Src)
		}
	}
	sort.Slice(c.Sources, func(i, j int) bool {
		return describeSource(c.Sources[i]) < describeSource(c.Sources[j])
	})
	avail := make([]string, 0, len(st.versions))
	for _, v := range st.versions {
		avail = append(avail, v.String())
	}
	msg := c.String()
	if len(avail) > 0 {
		msg += "\n  available versions in lane " + st.lane.Key() + ": " + strings.Join(avail, ", ")
	} else {
		msg += "\n  no versions exist in lane " + st.lane.Key()
	}
	return &Failure{Kind: KindConflict, Message: msg, Contradictions: []Contradiction{c},
		Package: st.lane.Package}
}
