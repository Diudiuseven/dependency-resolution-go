package resolver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version 表示一个语义化版本号。
type Version struct {
	Major int
	Minor int
	Patch int
	Pre   []string // 预发布标识符，按顺序排列
	Raw   string   // 原始字符串
}

// ParseVersion 解析语义化版本字符串。
// 支持 1.2.3、v1.2.3、1.2.3-beta.1 等格式。
func ParseVersion(s string) (Version, error) {
	raw := s
	s = strings.TrimSpace(s)
	if s == "" {
		return Version{}, fmt.Errorf("空版本字符串")
	}
	// 去掉前导 v
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	// 分离预发布部分
	var pre []string
	main := s
	if idx := strings.IndexByte(s, '-'); idx >= 0 {
		main = s[:idx]
		preStr := s[idx+1:]
		if preStr == "" {
			return Version{}, fmt.Errorf("非法版本 %q: 预发布部分为空", raw)
		}
		pre = strings.Split(preStr, ".")
		for _, p := range pre {
			if p == "" {
				return Version{}, fmt.Errorf("非法版本 %q: 预发布标识符为空", raw)
			}
		}
	}
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("非法版本 %q: 需要 major.minor.patch 格式", raw)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("非法版本 %q: %q 不是非负整数", raw, p)
		}
		nums[i] = n
	}
	return Version{
		Major: nums[0],
		Minor: nums[1],
		Patch: nums[2],
		Pre:   pre,
		Raw:   raw,
	}, nil
}

// MustParseVersion 解析失败时 panic，仅用于测试或初始化。
func MustParseVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

// Compare 比较两个版本：-1 表示 v < o，0 表示相等，1 表示 v > o。
// 预发布版本小于正式版本；预发布标识符按语义化版本规则比较。
func (v Version) Compare(o Version) int {
	if c := compareInt(v.Major, o.Major); c != 0 {
		return c
	}
	if c := compareInt(v.Minor, o.Minor); c != 0 {
		return c
	}
	if c := compareInt(v.Patch, o.Patch); c != 0 {
		return c
	}
	// 预发布比较：无预发布 > 有预发布
	if len(v.Pre) == 0 && len(o.Pre) == 0 {
		return 0
	}
	if len(v.Pre) == 0 {
		return 1
	}
	if len(o.Pre) == 0 {
		return -1
	}
	// 逐标识符比较
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := comparePreIdentifier(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	return compareInt(len(v.Pre), len(o.Pre))
}

func compareInt(a, b int) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// comparePreIdentifier 比较预发布标识符：数字按数值比较，字符串按字典序，数字 < 字符串。
func comparePreIdentifier(a, b string) int {
	aNum, aErr := strconv.Atoi(a)
	bNum, bErr := strconv.Atoi(b)
	if aErr == nil && bErr == nil {
		return compareInt(aNum, bNum)
	}
	if aErr == nil {
		return -1 // 数字 < 字符串
	}
	if bErr == nil {
		return 1
	}
	return strings.Compare(a, b)
}

// String 返回版本的字符串表示。
func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

// IsPrerelease 判断是否为预发布版本。
func (v Version) IsPrerelease() bool {
	return len(v.Pre) > 0
}

// Equal 判断两个版本是否相等。
func (v Version) Equal(o Version) bool {
	return v.Compare(o) == 0
}

// Less 判断 v 是否小于 o。
func (v Version) Less(o Version) bool {
	return v.Compare(o) < 0
}
