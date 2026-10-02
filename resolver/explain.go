package resolver

import (
	"fmt"
	"sort"
	"strings"
)

// ExplainConflict 生成可读的冲突说明，指明矛盾来源。
func ExplainConflict(err *ConflictError) string {
	var b strings.Builder
	b.WriteString("无法同时满足以下约束:\n")
	for _, cs := range err.Constraints {
		b.WriteString("  - ")
		b.WriteString(cs.Source.String())
		b.WriteString(" 声明 ")
		b.WriteString(err.Package)
		b.WriteString("@")
		b.WriteString(cs.Constraint.String())
		b.WriteString("\n")
	}
	b.WriteString("矛盾来源: ")
	sources := make([]string, 0, len(err.Constraints))
	seen := make(map[string]bool)
	for _, cs := range err.Constraints {
		key := cs.Source.String()
		if !seen[key] {
			seen[key] = true
			sources = append(sources, key)
		}
	}
	b.WriteString(strings.Join(sources, ", "))
	if err.Message != "" {
		b.WriteString("\n说明: ")
		b.WriteString(err.Message)
	}
	return b.String()
}

// ExplainResolution 生成求解结果的可读说明。
func ExplainResolution(r *Resolution) string {
	var b strings.Builder
	b.WriteString("求解结果:\n")
	// 按名称分组
	byName := make(map[string][]*Slot)
	var names []string
	for _, slot := range r.Slots {
		if _, ok := byName[slot.Key.Name]; !ok {
			names = append(names, slot.Key.Name)
		}
		byName[slot.Key.Name] = append(byName[slot.Key.Name], slot)
	}
	sort.Strings(names)
	for _, name := range names {
		slots := byName[name]
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteString(":")
		for i, slot := range slots {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(" ")
			b.WriteString(slot.Selected.String())
			b.WriteString(" (boundary=")
			b.WriteString(slot.Key.Boundary)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// ExplainCycle 生成环的可读说明。
func ExplainCycle(err *CycleError) string {
	return fmt.Sprintf("检测到依赖环: %s\n请检查相关包的依赖声明，避免循环依赖。", strings.Join(err.Path, " -> "))
}

// ExplainMissingDep 生成依赖缺失的可读说明。
func ExplainMissingDep(err *MissingDepError) string {
	return fmt.Sprintf("依赖缺失: %s (来源: %s)\n请确保该包在本地 registry 中可用。", err.Name, err.Source.String())
}

// ExplainInvalidRange 生成非法版本范围的可读说明。
func ExplainInvalidRange(err *InvalidRangeError) string {
	return fmt.Sprintf("非法版本范围: %q (来源: %s)\n请检查版本范围语法。", err.Range, err.Source.String())
}
