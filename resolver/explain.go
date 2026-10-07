package resolver

import (
	"fmt"
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
//
// 内容范围与边界情形：
//   - 每个槽位单独一行，按 (包名, 兼容边界) 升序排列；同一输入多次输出逐字节一致。
//   - 已落到具体版本的槽位输出版本与边界；最终没有落到具体版本的槽位
//     （例如求解过程中尝试过、随后因回溯被放弃的槽位）不会被跳过，
//     而是如实标注为“未选定具体版本”。
//   - 末尾给出汇总，显式列出未选定版本的依赖，保证读者能看到全部槽位的去向。
func ExplainResolution(r *Resolution) string {
	var b strings.Builder
	b.WriteString("求解结果:\n")

	// 直接使用 resolution() 已排序的槽位顺序（名称、边界升序）。
	// 不额外借助 map，避免遍历顺序不确定。
	resolved := 0
	var unresolved []string
	for _, slot := range r.Slots {
		b.WriteString("  ")
		b.WriteString(slot.Key.Name)
		b.WriteString(" (boundary=")
		b.WriteString(slot.Key.Boundary)
		b.WriteString("): ")
		if slot.Selected != nil {
			b.WriteString(slot.Selected.String())
			resolved++
		} else {
			b.WriteString("未选定具体版本（求解过程中尝试过，但未进入最终版本组合）")
			unresolved = append(unresolved, fmt.Sprintf("%s (boundary=%s)", slot.Key.Name, slot.Key.Boundary))
		}
		b.WriteString("\n")
	}

	b.WriteString("汇总: 共 ")
	b.WriteString(fmt.Sprintf("%d", len(r.Slots)))
	b.WriteString(" 个槽位，已选定版本 ")
	b.WriteString(fmt.Sprintf("%d", resolved))
	b.WriteString(" 个，未选定版本 ")
	b.WriteString(fmt.Sprintf("%d", len(unresolved)))
	b.WriteString(" 个")
	if len(unresolved) > 0 {
		// unresolved 的顺序由前面按 (名称, 边界) 的遍历决定，天然确定。
		b.WriteString("；未选定版本的依赖: ")
		b.WriteString(strings.Join(unresolved, ", "))
	}
	b.WriteString("\n")
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
