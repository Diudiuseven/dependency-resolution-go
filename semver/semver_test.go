package semver

import "testing"

func TestParseCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.1", -1},
		{"1.2.3", "1.2.3", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-rc.1+build.5", "1.0.0-rc.1", 0},
		{"0.0.1", "0.0.10", -1},
	}
	for _, c := range cases {
		a, err := Parse(c.a)
		if err != nil {
			t.Fatalf("parse %s: %v", c.a, err)
		}
		b, err := Parse(c.b)
		if err != nil {
			t.Fatalf("parse %s: %v", c.b, err)
		}
		if got := a.Compare(b); got != c.want {
			t.Errorf("Compare(%q,%q)=%d want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	for _, s := range []string{"", "1", "01.0.0", "1.2", "1.2.3.4", "v1.2.3", "1.2.3-", "1.2.3+"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) expected error", s)
		}
	}
}

func TestRangeContains(t *testing.T) {
	cases := []struct {
		rng string
		v   string
		ok  bool
	}{
		{"^1.2.3", "1.2.5", true},
		{"^1.2.3", "1.9.0", true},
		{"^1.2.3", "2.0.0", false},
		{"^0.2.3", "0.2.9", true},
		{"^0.2.3", "0.3.0", false},
		{"^0.0.3", "0.0.3", true},
		{"^0.0.3", "0.0.4", false},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{">=1.0 <2.0", "1.9.9", true},
		{">=1.0 <2.0", "2.0.0", false},
		{"1.x", "1.4.0", true},
		{"1.x", "2.0.0", false},
		{"1.2.*", "1.2.0", true},
		{"1.2.*", "1.3.0", false},
		{"1.2", "1.2.9", true},
		{"1.2", "1.3.0", false},
		{"1.0.0 || 2.0.0", "2.0.0", true},
		{"1.0.0 || 2.0.0", "3.0.0", false},
		{"*", "1.2.3", true},
		{"*", "1.2.3-alpha", false},
		{">=1.0.0-alpha", "1.2.3-alpha", true},
		{"!=1.2.3", "1.2.4", true},
	}
	for _, c := range cases {
		r, err := ParseRange(c.rng)
		if err != nil {
			t.Fatalf("ParseRange(%q): %v", c.rng, err)
		}
		v := MustParse(c.v)
		if got := r.Contains(v); got != c.ok {
			t.Errorf("Contains(%q in %q)=%v want %v", c.v, c.rng, got, c.ok)
		}
	}
}

func TestRangeInvalid(t *testing.T) {
	for _, s := range []string{">", "^", "1.x.2", "1.2.3.4 || 1.0.0", ">=abc"} {
		if _, err := ParseRange(s); err == nil {
			t.Errorf("ParseRange(%q) expected error", s)
		}
	}
}

func TestRangeMajors(t *testing.T) {
	r := MustParseRange(t, "^1.0.0 || ^2.0.0")
	ms, ok := r.Majors()
	if !ok || len(ms) != 2 || ms[0] != 1 || ms[1] != 2 {
		t.Fatalf("majors = %v ok=%v", ms, ok)
	}
	if _, ok := MustParseRange(t, ">=1.0.0").Majors(); ok {
		t.Fatal("open range should report ok=false")
	}
}

func MustParseRange(t *testing.T, s string) Range {
	t.Helper()
	r, err := ParseRange(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
