package resolver

import (
	"fmt"
	"strconv"
	"strings"
)

// Constraint 表示一个版本约束。
type Constraint interface {
	// Satisfies 判断给定版本是否满足约束。
	Satisfies(v Version) bool
	// String 返回约束的字符串表示。
	String() string
}

// ParseConstraint 解析版本范围字符串。
// 支持: ^1.2.3, ~1.2.3, >=1.0.0 <2.0.0, 1.2.3, *, 1.x, 1.2.x, >=1.0.0, 以及 || 析取。
func ParseConstraint(s string) (Constraint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("空约束字符串")
	}
	// 处理 || 析取
	if strings.Contains(s, "||") {
		parts := strings.Split(s, "||")
		var orConstraints []Constraint
		for _, p := range parts {
			c, err := parseConstraint(strings.TrimSpace(p))
			if err != nil {
				return nil, err
			}
			orConstraints = append(orConstraints, c)
		}
		return &OrConstraint{Constraints: orConstraints}, nil
	}
	return parseConstraint(s)
}

func parseConstraint(s string) (Constraint, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("空约束字符串")
	}
	// 通配符 *
	if s == "*" || s == "x" || s == "X" {
		return &AnyConstraint{}, nil
	}
	// 1.x 或 1.2.x
	if strings.HasSuffix(s, ".x") || strings.HasSuffix(s, ".X") || strings.HasSuffix(s, ".*") {
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(s, ".x"), ".X"), ".*")
		parts := strings.Split(base, ".")
		if len(parts) > 2 {
			return nil, fmt.Errorf("非法通配符约束 %q", s)
		}
		major, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, fmt.Errorf("非法通配符约束 %q: %v", s, err)
		}
		w := &WildcardConstraint{Major: major}
		if len(parts) == 2 {
			minor, err := strconv.Atoi(parts[1])
			if err != nil {
				return nil, fmt.Errorf("非法通配符约束 %q: %v", s, err)
			}
			w.Minor = &minor
		}
		return w, nil
	}
	// 插入号 ^
	if strings.HasPrefix(s, "^") {
		v, err := ParseVersion(strings.TrimPrefix(s, "^"))
		if err != nil {
			return nil, fmt.Errorf("非法插入号约束 %q: %v", s, err)
		}
		return &CaretConstraint{Version: v}, nil
	}
	// 波浪号 ~
	if strings.HasPrefix(s, "~") {
		v, err := ParseVersion(strings.TrimPrefix(s, "~"))
		if err != nil {
			return nil, fmt.Errorf("非法波浪号约束 %q: %v", s, err)
		}
		return &TildeConstraint{Version: v}, nil
	}
	// 比较级或精确版本
	comparators, err := parseComparators(s)
	if err != nil {
		return nil, err
	}
	if len(comparators) == 1 && comparators[0].Op == "=" {
		return &ExactConstraint{Version: comparators[0].Ver}, nil
	}
	return &RangeConstraint{Comparators: comparators}, nil
}

// Comparator 单个比较级，如 >=1.0.0。
type Comparator struct {
	Op  string // ">=", "<", ">", "<=", "="
	Ver Version
}

// parseComparators 解析比较级列表，如 ">=1.0.0 <2.0.0" 或 "1.2.3"。
func parseComparators(s string) ([]Comparator, error) {
	tokens := strings.Fields(s)
	var comparators []Comparator
	for _, tok := range tokens {
		op := "="
		verStr := tok
		for _, prefix := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(tok, prefix) {
				op = prefix
				verStr = strings.TrimPrefix(tok, prefix)
				break
			}
		}
		v, err := ParseVersion(verStr)
		if err != nil {
			return nil, fmt.Errorf("非法比较级 %q: %v", tok, err)
		}
		comparators = append(comparators, Comparator{Op: op, Ver: v})
	}
	return comparators, nil
}

// ExactConstraint 精确版本约束。
type ExactConstraint struct{ Version Version }

func (c *ExactConstraint) Satisfies(v Version) bool {
	return v.Compare(c.Version) == 0
}
func (c *ExactConstraint) String() string { return c.Version.String() }

// CaretConstraint 插入号约束（兼容大版本）。
// ^1.2.3 := >=1.2.3 <2.0.0；^0.2.3 := >=0.2.3 <0.3.0；^0.0.3 := >=0.0.3 <0.0.4。
type CaretConstraint struct{ Version Version }

func (c *CaretConstraint) Satisfies(v Version) bool {
	if v.Compare(c.Version) < 0 {
		return false
	}
	upper := c.Version
	if upper.Major > 0 {
		upper.Major++
		upper.Minor = 0
		upper.Patch = 0
	} else if upper.Minor > 0 {
		upper.Minor++
		upper.Patch = 0
	} else {
		upper.Patch++
	}
	return v.Compare(upper) < 0
}
func (c *CaretConstraint) String() string { return "^" + c.Version.String() }

// TildeConstraint 波浪号约束（兼容小版本）。
// ~1.2.3 := >=1.2.3 <1.3.0。
type TildeConstraint struct{ Version Version }

func (c *TildeConstraint) Satisfies(v Version) bool {
	if v.Compare(c.Version) < 0 {
		return false
	}
	upper := c.Version
	upper.Minor++
	upper.Patch = 0
	return v.Compare(upper) < 0
}
func (c *TildeConstraint) String() string { return "~" + c.Version.String() }

// RangeConstraint 比较级约束的合取（AND）。
type RangeConstraint struct{ Comparators []Comparator }

func (c *RangeConstraint) Satisfies(v Version) bool {
	for _, cmp := range c.Comparators {
		if !cmp.satisfies(v) {
			return false
		}
	}
	return true
}
func (c *RangeConstraint) String() string {
	parts := make([]string, len(c.Comparators))
	for i, cmp := range c.Comparators {
		parts[i] = cmp.Op + cmp.Ver.String()
	}
	return strings.Join(parts, " ")
}

func (cmp Comparator) satisfies(v Version) bool {
	switch cmp.Op {
	case ">=":
		return v.Compare(cmp.Ver) >= 0
	case "<=":
		return v.Compare(cmp.Ver) <= 0
	case ">":
		return v.Compare(cmp.Ver) > 0
	case "<":
		return v.Compare(cmp.Ver) < 0
	case "=":
		return v.Compare(cmp.Ver) == 0
	}
	return false
}

// AnyConstraint 任意版本约束（*）。
type AnyConstraint struct{}

func (c *AnyConstraint) Satisfies(v Version) bool { return true }
func (c *AnyConstraint) String() string          { return "*" }

// WildcardConstraint 通配符约束，如 1.x 或 1.2.x。
type WildcardConstraint struct {
	Major int
	Minor *int // nil 表示 1.x，否则表示 1.2.x
}

func (c *WildcardConstraint) Satisfies(v Version) bool {
	if v.Major != c.Major {
		return false
	}
	if c.Minor != nil && v.Minor != *c.Minor {
		return false
	}
	return true
}
func (c *WildcardConstraint) String() string {
	if c.Minor == nil {
		return fmt.Sprintf("%d.x", c.Major)
	}
	return fmt.Sprintf("%d.%d.x", c.Major, *c.Minor)
}

// OrConstraint 析取约束（||）。
type OrConstraint struct{ Constraints []Constraint }

func (c *OrConstraint) Satisfies(v Version) bool {
	for _, sub := range c.Constraints {
		if sub.Satisfies(v) {
			return true
		}
	}
	return false
}
func (c *OrConstraint) String() string {
	parts := make([]string, len(c.Constraints))
	for i, sub := range c.Constraints {
		parts[i] = sub.String()
	}
	return strings.Join(parts, " || ")
}
