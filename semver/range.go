package semver

import (
	"fmt"
	"sort"
	"strings"
)

// Range is a parsed version constraint. It is a disjunction (OR) of groups;
// each group is an intersection (AND) of comparison terms. An empty range
// with no groups matches every released version.
type Range struct {
	raw    string
	groups []group
}

type group struct {
	terms []term
}

type termOp int

const (
	opEq termOp = iota
	opNe
	opGt
	opGte
	opLt
	opLte
	opCaret
	opTilde
)

type term struct {
	op termOp
	v  Version
}

func (r Range) String() string { return r.raw }

// Empty reports whether the range has no constraints ("*" or "").
func (r Range) Empty() bool { return len(r.groups) == 0 }

// Contains reports whether v satisfies the range. Pre-release versions are
// only matched when the range itself contains a term with the same
// major.minor.patch core and a pre-release suffix (npm/cargo-style guard).
func (r Range) Contains(v Version) bool {
	if r.Empty() {
		return v.IsRelease() || r.allowsPre(v)
	}
	for _, g := range r.groups {
		if groupContains(g, v) {
			return true
		}
	}
	return false
}

// allowsPre is used for unconstrained ("*") ranges: pre-releases are
// excluded by default but accepted when any explicit term carries a
// pre-release on the same core version.
func (r Range) allowsPre(v Version) bool {
	if v.IsRelease() {
		return true
	}
	for _, g := range r.groups {
		for _, t := range g.terms {
			if !t.v.IsRelease() && sameCore(t.v, v) {
				return true
			}
		}
	}
	return false
}

func sameCore(a, b Version) bool {
	return a.Major == b.Major && a.Minor == b.Minor && a.Patch == b.Patch
}

func groupContains(g group, v Version) bool {
	if !v.IsRelease() && !groupMentionsPre(g, v) {
		return false
	}
	for _, t := range g.terms {
		if !termContains(t, v) {
			return false
		}
	}
	return true
}

// groupMentionsPre returns true when the group references a prerelease
// bound on the same major as v (npm-style prerelease admission).
func groupMentionsPre(g group, v Version) bool {
	for _, t := range g.terms {
		if t.v.IsRelease() {
			continue
		}
		if t.v.Major == v.Major {
			return true
		}
	}
	return false
}

func termContains(t term, v Version) bool {
	// Prerelease admission (npm-style): a prerelease candidate is only
	// reachable through a comparison whose bound is itself a prerelease.
	// When the bound is a prerelease, open comparisons decide on the
	// numeric core tuple so that e.g. ">=1.0.0-alpha" admits "1.2.3-alpha"
	// even though 1.2.3-alpha < 1.0.0 in full semver precedence.
	prereleaseBound := !t.v.IsRelease()
	coreCmp := compareCore(v, t.v)
	switch t.op {
	case opEq:
		return v.Compare(t.v) == 0
	case opNe:
		return v.Compare(t.v) != 0
	case opGt:
		if !v.IsRelease() && prereleaseBound {
			if coreCmp > 0 {
				return true
			}
			if coreCmp == 0 {
				return v.Compare(t.v) > 0
			}
			return false
		}
		return v.Compare(t.v) > 0
	case opGte:
		if !v.IsRelease() && prereleaseBound {
			if coreCmp > 0 {
				return true
			}
			if coreCmp == 0 {
				return v.Compare(t.v) >= 0
			}
			return false
		}
		return v.Compare(t.v) >= 0
	case opLt:
		if !v.IsRelease() && prereleaseBound && coreCmp < 0 {
			return true
		}
		return v.Compare(t.v) < 0
	case opLte:
		if !v.IsRelease() && prereleaseBound && coreCmp < 0 {
			return true
		}
		return v.Compare(t.v) <= 0
	case opCaret:
		return caretSatisfies(t.v, v)
	case opTilde:
		return tildeSatisfies(t.v, v)
	}
	return false
}

// compareCore compares just the major.minor.patch tuple (-1/0/1).
func compareCore(a, b Version) int {
	if c := cmpUint(a.Major, b.Major); c != 0 {
		return c
	}
	if c := cmpUint(a.Minor, b.Minor); c != 0 {
		return c
	}
	return cmpUint(a.Patch, b.Patch)
}

// caretSatisfies implements ^: >=v, <next-breaking, where "breaking" is the
// next major for >=1.0.0, the next minor for 0.x (x>0), and the next patch
// for 0.0.z.
func caretSatisfies(base, v Version) bool {
	if v.Compare(base) < 0 {
		return false
	}
	switch {
	case base.Major > 0:
		return v.Major == base.Major
	case base.Minor > 0:
		return v.Major == 0 && v.Minor == base.Minor
	default:
		return v.Major == 0 && v.Minor == 0 && v.Patch == base.Patch
	}
}

// tildeSatisfies implements ~: >=v, <next-minor (or <next-major when only
// the major component is supplied).
func tildeSatisfies(base, v Version) bool {
	if v.Compare(base) < 0 {
		return false
	}
	if v.Major != base.Major {
		return false
	}
	return v.Minor == base.Minor
}

// Intersect returns a range matching the intersection of r and o. The
// result is a normalized disjunction of intersections: each group of r is
// paired with each group of o and their terms concatenated.
func (r Range) Intersect(o Range) Range {
	switch {
	case r.Empty():
		return Range{raw: mergeRaw(r.raw, o.raw), groups: cloneGroups(o.groups)}
	case o.Empty():
		return Range{raw: mergeRaw(r.raw, o.raw), groups: cloneGroups(r.groups)}
	}
	groups := make([]group, 0, len(r.groups)*len(o.groups))
	for _, a := range r.groups {
		for _, b := range o.groups {
			g := group{terms: make([]term, 0, len(a.terms)+len(b.terms))}
			g.terms = append(g.terms, a.terms...)
			g.terms = append(g.terms, b.terms...)
			groups = append(groups, g)
		}
	}
	return Range{raw: mergeRaw(r.raw, o.raw), groups: groups}
}

func mergeRaw(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return "(" + a + ") (" + b + ")"
	}
}

func cloneGroups(gs []group) []group {
	out := make([]group, len(gs))
	for i, g := range gs {
		out[i] = group{terms: append([]term(nil), g.terms...)}
	}
	return out
}

// Majors returns the set of concrete major versions allowed by the range.
// A range that does not pin the major (e.g. ">=1.0.0", "*", "1.x || 2.x"
// with an open-ended group) reports ok=false. Negations (!=) make a group
// open-ended.
func (r Range) Majors() (majors []uint64, ok bool) {
	if r.Empty() {
		return nil, false
	}
	seen := map[uint64]struct{}{}
	for _, g := range r.groups {
		ms, gok := groupMajors(g)
		if !gok {
			return nil, false
		}
		for _, m := range ms {
			seen[m] = struct{}{}
		}
	}
	out := make([]uint64, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, true
}

func groupMajors(g group) ([]uint64, bool) {
	var allowed map[uint64]struct{}
	restrict := func(ms []uint64) {
		set := map[uint64]struct{}{}
		for _, m := range ms {
			set[m] = struct{}{}
		}
		if allowed == nil {
			allowed = set
			return
		}
		for m := range allowed {
			if _, ok := set[m]; !ok {
				delete(allowed, m)
			}
		}
	}
	for _, t := range g.terms {
		switch t.op {
		case opEq:
			restrict([]uint64{t.v.Major})
		case opCaret:
			restrict([]uint64{t.v.Major})
		case opTilde:
			restrict([]uint64{t.v.Major})
		case opGt, opGte, opLt, opLte, opNe:
			return nil, false
		}
	}
	if allowed == nil {
		return nil, false
	}
	out := make([]uint64, 0, len(allowed))
	for m := range allowed {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, true
}

// ParseRange parses a constraint string. Supported syntax:
//
//	"*", ""                 any released version
//	"1.2.3", "=1.2.3"      exact
//	"^1.2.3"               caret
//	"~1.2.3"               tilde
//	">=1.0 <2.0"           comparisons joined by whitespace/comma (AND)
//	"1.x", "1.2.*"         wildcard
//	"a || b"               disjunction (OR)
//
// Multiple whitespace/comma separated atoms are intersected.
func ParseRange(s string) (Range, error) {
	raw := s
	s = strings.TrimSpace(s)
	if s == "" || s == "*" || strings.EqualFold(s, "latest") {
		return Range{raw: raw}, nil
	}
	for _, orPart := range strings.Split(s, "||") {
		if _, err := parseGroup(strings.TrimSpace(orPart)); err != nil {
			return Range{}, err
		}
	}
	groups := make([]group, 0)
	for _, orPart := range strings.Split(s, "||") {
		g, err := parseGroup(strings.TrimSpace(orPart))
		if err != nil {
			return Range{}, err
		}
		groups = append(groups, g)
	}
	return Range{raw: raw, groups: groups}, nil
}

func parseGroup(s string) (group, error) {
	if s == "" {
		return group{}, fmt.Errorf("invalid range: empty alternative")
	}
	atoms := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' })
	g := group{}
	for _, a := range atoms {
		if a == "" || a == "*" {
			continue
		}
		t, err := parseAtom(a)
		if err != nil {
			return group{}, err
		}
		g.terms = append(g.terms, t...)
	}
	return g, nil
}

// parseAtom parses one constraint atom, possibly expanding wildcards into
// two terms (>=lower, <upper).
func parseAtom(a string) ([]term, error) {
	// Operators.
	for _, prefix := range []string{">=", "<=", "==", "!=", ">", "<", "=", "^", "~"} {
		if strings.HasPrefix(a, prefix) {
			num := strings.TrimSpace(a[len(prefix):])
			v, err := parsePartial(num)
			if err != nil {
				return nil, fmt.Errorf("invalid range %q: %w", a, err)
			}
			switch prefix {
			case "^":
				return []term{{opCaret, v}}, nil
			case "~":
				return []term{{opTilde, v}}, nil
			case ">=":
				return []term{{opGte, v}}, nil
			case "<=":
				return []term{{opLte, v}}, nil
			case ">":
				return []term{{opGt, v}}, nil
			case "<":
				return []term{{opLt, v}}, nil
			case "==", "=":
				return []term{{opEq, v}}, nil
			case "!=":
				return []term{{opNe, v}}, nil
			}
		}
	}
	// Wildcards: 1.x, 1.2.*, 1.x-beta? (pre on wildcard unsupported).
	if strings.Contains(a, "x") || strings.Contains(a, "X") || strings.Contains(a, "*") {
		return parseWildcard(a)
	}
	// Bare partial version:
	//   "1.2.3" -> exact
	//   "1.2"   -> >=1.2.0 <1.3.0
	//   "1"     -> >=1.0.0 <2.0.0
	return parseBare(a)
}

// parsePartial parses "1", "1.2", "1.2.3" with optional pre/build into a
// full Version, zero-filling missing components.
func parsePartial(s string) (Version, error) {
	// Split off pre/build before padding components.
	core := s
	var suffix string
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core, suffix = core[:i], core[i:]
	}
	parts := strings.Split(core, ".")
	if len(parts) > 3 {
		return Version{}, fmt.Errorf("too many version components in %q", s)
	}
	for _, p := range parts {
		if p == "" {
			return Version{}, fmt.Errorf("empty version component in %q", s)
		}
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return Parse(strings.Join(parts, ".") + suffix)
}

func parseBare(a string) ([]term, error) {
	dots := strings.Count(strings.SplitN(a, "-", 2)[0], ".")
	if dots == 2 {
		v, err := Parse(a)
		if err != nil {
			return nil, fmt.Errorf("invalid range %q: %w", a, err)
		}
		return []term{{opEq, v}}, nil
	}
	// Partial bare versions act as a >=lower,<upper window.
	lo, hi, bounded, err := wildcardBounds(a)
	if err != nil {
		return nil, err
	}
	if !bounded {
		return nil, nil
	}
	return []term{{opGte, lo}, {opLt, hi}}, nil
}

func parseWildcard(a string) ([]term, error) {
	lo, hi, bounded, err := wildcardBounds(a)
	if err != nil {
		return nil, err
	}
	if !bounded {
		return nil, nil // "*" alone: no terms
	}
	return []term{{opGte, lo}, {opLt, hi}}, nil
}

// wildcardBounds returns [lo, hi) bounds for strings like "1.x",
// "1.2.*", "1", "1.2". bounded is false for a pure wildcard ("*").
func wildcardBounds(a string) (lo, hi Version, bounded bool, err error) {
	core := a
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		return Version{}, Version{}, false, fmt.Errorf("prerelease not allowed with wildcard in %q", a)
	}
	parts := strings.Split(core, ".")
	if len(parts) > 3 {
		return Version{}, Version{}, false, fmt.Errorf("too many version components in %q", a)
	}
	// Find first wildcard component; everything after must be wildcard.
	firstWild := -1
	for i, p := range parts {
		if p == "x" || p == "X" || p == "*" {
			if firstWild < 0 {
				firstWild = i
			}
		} else if firstWild >= 0 {
			return Version{}, Version{}, false, fmt.Errorf("version component after wildcard in %q", a)
		}
	}
	numCount := len(parts)
	if firstWild >= 0 {
		numCount = firstWild
	}
	nums := make([]uint64, 3)
	for i := 0; i < numCount; i++ {
		u, perr := parseUintComp(parts[i])
		if perr != nil {
			return Version{}, Version{}, false, fmt.Errorf("invalid range %q: %w", a, perr)
		}
		nums[i] = u
	}
	// All wildcard ("*", "x"): no bounds at all.
	if numCount == 0 {
		return Version{}, Version{}, false, nil
	}
	lo = MustParse(fmt.Sprintf("%d.%d.%d", nums[0], nums[1], nums[2]))
	switch numCount {
	case 1:
		hi = MustParse(fmt.Sprintf("%d.0.0", nums[0]+1))
	case 2:
		hi = MustParse(fmt.Sprintf("%d.%d.0", nums[0], nums[1]+1))
	default:
		hi = MustParse(fmt.Sprintf("%d.%d.%d", nums[0], nums[1], nums[2]+1))
	}
	return lo, hi, true, nil
}

func parseUintComp(s string) (uint64, error) {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("expected digit, got %q", s)
		}
	}
	var u uint64
	for i := 0; i < len(s); i++ {
		u = u*10 + uint64(s[i]-'0')
	}
	return u, nil
}
