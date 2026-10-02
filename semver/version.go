// Package semver provides semantic version parsing, comparison and version
// range matching. Everything in this package is deterministic and free of
// external dependencies.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version: major.minor.patch with optional
// pre-release identifiers. Build metadata is retained but never participates
// in precedence (per semver spec).
type Version struct {
	Major    uint64
	Minor    uint64
	Patch    uint64
	Pre      []Identifier
	pre      string
	build    string
	original string
}

// Identifier is one dot-separated pre-release identifier.
type Identifier struct {
	IsNum bool
	Num   uint64
	Text  string
}

func (v Version) String() string {
	if v.original != "" {
		return v.original
	}
	s := v.Core()
	if v.pre != "" {
		s += "-" + v.pre
	}
	if v.build != "" {
		s += "+" + v.build
	}
	return s
}

// Core returns major.minor.patch without pre-release or build metadata.
func (v Version) Core() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// PreString returns the raw pre-release suffix (without leading '-').
func (v Version) PreString() string { return v.pre }

// IsRelease reports whether the version has no pre-release suffix.
func (v Version) IsRelease() bool { return v.pre == "" }

// Compare returns -1, 0 or 1.
func (v Version) Compare(o Version) int {
	if c := cmpUint(v.Major, o.Major); c != 0 {
		return c
	}
	if c := cmpUint(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := cmpUint(v.Patch, o.Patch); c != 0 {
		return c
	}
	return comparePre(v.Pre, o.Pre)
}

// Equal reports exact precedence equality (build metadata ignored).
func (v Version) Equal(o Version) bool { return v.Compare(o) == 0 }

// Less reports whether v has lower precedence than o.
func (v Version) Less(o Version) bool { return v.Compare(o) < 0 }

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePre implements semver pre-release precedence:
//   - a version without pre-release has higher precedence;
//   - numeric identifiers have lower precedence than alphanumeric;
//   - numeric identifiers compare as integers;
//   - alphanumeric compare lexically (ASCII);
//   - a longer set wins when all shared identifiers are equal.
func comparePre(a, b []Identifier) int {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	if len(a) == 0 {
		return 1 // a is release, higher
	}
	if len(b) == 0 {
		return -1
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		x, y := a[i], b[i]
		switch {
		case x.IsNum && y.IsNum:
			if c := cmpUint(x.Num, y.Num); c != 0 {
				return c
			}
		case x.IsNum && !y.IsNum:
			return -1
		case !x.IsNum && y.IsNum:
			return 1
		default:
			if x.Text != y.Text {
				if x.Text < y.Text {
					return -1
				}
				return 1
			}
		}
	}
	return cmpUint(uint64(len(a)), uint64(len(b)))
}

// Parse parses a semantic version string in canonical X.Y.Z form with
// optional "-prerelease" and "+build" suffixes. Partial forms ("1", "1.2")
// are rejected here; range parsing accepts them separately.
func Parse(s string) (Version, error) {
	v, rest, err := parseLeadingVersion(s, true)
	if err != nil {
		return Version{}, err
	}
	rest, err = consumeSuffixes(&v, s, rest)
	if err != nil {
		return Version{}, err
	}
	if rest != "" {
		return Version{}, fmt.Errorf("invalid version %q: unexpected trailing text %q", s, rest)
	}
	v.original = s
	return v, nil
}

// consumeSuffixes parses an optional "-prerelease" and "+build" part.
func consumeSuffixes(v *Version, full, rest string) (string, error) {
	if rest == "" {
		return rest, nil
	}
	if rest[0] == '-' {
		end := strings.IndexAny(rest, "+")
		var pre string
		if end < 0 {
			pre, rest = rest[1:], ""
		} else {
			pre, rest = rest[1:end], rest[end:]
		}
		ids, err := parseIdentifiers(full, pre)
		if err != nil {
			return "", err
		}
		v.Pre = ids
		v.pre = pre
	}
	if strings.HasPrefix(rest, "+") {
		build := rest[1:]
		if build == "" || strings.Contains(build, "+") {
			return "", fmt.Errorf("invalid version %q: malformed build metadata", full)
		}
		for _, part := range strings.Split(build, ".") {
			if part == "" || !isAllIdentChars(part) {
				return "", fmt.Errorf("invalid version %q: malformed build identifier %q", full, part)
			}
		}
		v.build = build
		rest = ""
	}
	return rest, nil
}

func isAllIdentChars(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-') {
			return false
		}
	}
	return true
}

func parseIdentifiers(full, pre string) ([]Identifier, error) {
	parts := strings.Split(pre, ".")
	ids := make([]Identifier, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil, fmt.Errorf("invalid version %q: empty prerelease identifier", full)
		}
		numeric := true
		for i := 0; i < len(part); i++ {
			if part[i] < '0' || part[i] > '9' {
				numeric = false
				break
			}
		}
		if numeric {
			if len(part) > 1 && part[0] == '0' {
				return nil, fmt.Errorf("invalid version %q: numeric prerelease identifier %q has leading zero", full, part)
			}
			n, err := strconv.ParseUint(part, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid version %q: %w", full, err)
			}
			ids = append(ids, Identifier{IsNum: true, Num: n})
			continue
		}
		if !isAllIdentChars(part) {
			return nil, fmt.Errorf("invalid version %q: malformed prerelease identifier %q", full, part)
		}
		ids = append(ids, Identifier{IsNum: false, Text: part})
	}
	return ids, nil
}

// parseLeadingVersion consumes X.Y.Z (or X.Y / X when requireFull is false)
// and returns the remainder of the string.
func parseLeadingVersion(s string, requireFull bool) (Version, string, error) {
	if s == "" {
		return Version{}, "", fmt.Errorf("invalid version: empty string")
	}
	var v Version
	i := 0

	major, n, err := parseNumericComponent(s, "major")
	if err != nil {
		return Version{}, "", err
	}
	v.Major = major
	i = n
	if i == len(s) {
		if requireFull {
			return Version{}, "", fmt.Errorf("invalid version %q: missing minor and patch", s)
		}
		return v, "", nil
	}
	if s[i] != '.' {
		return Version{}, "", fmt.Errorf("invalid version %q: expected '.' at position %d", s, i)
	}
	i++
	minor, n, err := parseNumericComponent(s[i:], "minor")
	if err != nil {
		return Version{}, "", err
	}
	v.Minor = minor
	i += n
	if i == len(s) {
		if requireFull {
			return Version{}, "", fmt.Errorf("invalid version %q: missing patch", s)
		}
		return v, "", nil
	}
	if s[i] != '.' {
		return Version{}, s[i:], nil // partial: e.g. "1.2" followed by suffix
	}
	i++
	patch, n, err := parseNumericComponent(s[i:], "patch")
	if err != nil {
		return Version{}, "", err
	}
	v.Patch = patch
	i += n
	return v, s[i:], nil
}

func parseNumericComponent(s, label string) (uint64, int, error) {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return 0, 0, fmt.Errorf("invalid %s component: expected digit", label)
	}
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	part := s[:n]
	if len(part) > 1 && part[0] == '0' {
		return 0, 0, fmt.Errorf("invalid %s component %q: leading zeroes are not allowed", label, part)
	}
	u, err := strconv.ParseUint(part, 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid %s component %q: %w", label, part, err)
	}
	return u, n, nil
}

// MustParse parses or panics; intended for tests/constants.
func MustParse(s string) Version {
	v, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}
