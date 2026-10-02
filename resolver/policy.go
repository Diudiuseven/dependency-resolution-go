package resolver

import "strconv"

// CompatibilityPolicy 定义兼容判定规则。
// 规则决定版本如何分组（边界），以及约束如何满足。
type CompatibilityPolicy interface {
	// Name 返回规则名称。
	Name() string
	// Boundary 返回版本的分组边界标识。
	// 同一分组内的版本共享同一个求解槽位。
	Boundary(v Version) string
}

// MajorPolicy 按大版本分组（默认，语义化版本兼容）。
type MajorPolicy struct{}

// Boundary 实现 CompatibilityPolicy。
func (MajorPolicy) Boundary(v Version) string { return strconv.Itoa(v.Major) }

// Name 实现 CompatibilityPolicy。
func (MajorPolicy) Name() string { return "major" }

// MinorPolicy 按小版本分组。
type MinorPolicy struct{}

// Boundary 实现 CompatibilityPolicy。
func (MinorPolicy) Boundary(v Version) string { return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) }

// Name 实现 CompatibilityPolicy。
func (MinorPolicy) Name() string { return "minor" }

// ExactPolicy 按精确版本分组（不共享）。
type ExactPolicy struct{}

// Boundary 实现 CompatibilityPolicy。
func (ExactPolicy) Boundary(v Version) string { return v.String() }

// Name 实现 CompatibilityPolicy。
func (ExactPolicy) Name() string { return "exact" }

// UnifiedPolicy 所有版本共享一个槽位（类似 pip 全局单版本）。
type UnifiedPolicy struct{}

// Boundary 实现 CompatibilityPolicy。
func (UnifiedPolicy) Boundary(v Version) string { return "" }

// Name 实现 CompatibilityPolicy。
func (UnifiedPolicy) Name() string { return "unified" }

// 默认策略实例。
var (
	PolicyMajor   = MajorPolicy{}
	PolicyMinor   = MinorPolicy{}
	PolicyExact   = ExactPolicy{}
	PolicyUnified = UnifiedPolicy{}
)

// PolicyByName 根据名称查找策略。
func PolicyByName(name string) (CompatibilityPolicy, bool) {
	switch name {
	case "major":
		return PolicyMajor, true
	case "minor":
		return PolicyMinor, true
	case "exact":
		return PolicyExact, true
	case "unified":
		return PolicyUnified, true
	}
	return nil, false
}
