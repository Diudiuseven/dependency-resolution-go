package resolver

import (
	"strings"
	"testing"
)

// buildUnselectedSlotRegistry 构建一个会产生"未选定槽位"的 registry:
// lib 的 2.x 版本依赖不存在的 dep@^2.0.0（dep 只有 1.0.0），
// 因此边界 2 求解失败并回溯，但槽位 (lib, 2) 已创建且最终未选定版本；
// 边界 1 的 lib@1.0.0 求解成功，整体求解成功。
func buildUnselectedSlotRegistry() *MemoryRegistry {
	r := NewMemoryRegistry()
	r.AddPackage("lib", &PackageManifest{Name: "lib", Version: "1.0.0"})
	r.AddPackage("lib", &PackageManifest{Name: "lib", Version: "2.0.0", Dependencies: map[string]string{"dep": "^2.0.0"}})
	r.AddPackage("dep", &PackageManifest{Name: "dep", Version: "1.0.0"})
	return r
}

// solveWithUnselectedSlot 求解上述场景，返回求解结果。
func solveWithUnselectedSlot(t *testing.T) *Resolution {
	t.Helper()
	r := buildUnselectedSlotRegistry()
	s := NewSolver(r, PolicyMajor)
	res, err := s.Solve([]DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "lib", Constraint: ">=1.0.0"},
	})
	if err != nil {
		t.Fatalf("求解应成功, 实际错误: %v", err)
	}
	return res
}

// TestExplainResolutionWithUnselectedSlot 复现崩溃场景:
// 求解成功后结果中含有未选定版本的槽位，说明输出不得崩溃，且必须如实呈现该槽位。
func TestExplainResolutionWithUnselectedSlot(t *testing.T) {
	res := solveWithUnselectedSlot(t)

	// 前置条件: 结果中确实存在未选定槽位（这是触发崩溃的场景特征）
	var unselected *Slot
	for _, slot := range res.Slots {
		if slot.Selected == nil {
			unselected = slot
		}
	}
	if unselected == nil {
		t.Fatal("前置条件不满足: 求解结果中应存在未选定版本的槽位")
	}

	// 不得崩溃（旧实现会在此处空指针解引用）
	out := ExplainResolution(res)

	if !strings.Contains(out, "lib") {
		t.Fatalf("说明输出应包含包名 lib:\n%s", out)
	}
	if !strings.Contains(out, "1.0.0") {
		t.Fatalf("说明输出应包含已选定版本 1.0.0:\n%s", out)
	}
	if !strings.Contains(out, "<未选定>") {
		t.Fatalf("说明输出应如实呈现未选定槽位，不得跳过:\n%s", out)
	}
	if !strings.Contains(out, unselected.Key.Boundary) {
		t.Fatalf("说明输出应包含未选定槽位的边界 %q:\n%s", unselected.Key.Boundary, out)
	}
}

// TestExplainResolutionDeterministic 同一份求解结论，说明输出的内容与顺序必须一致，
// 且与 Resolution 内部槽位顺序无关。
func TestExplainResolutionDeterministic(t *testing.T) {
	res := solveWithUnselectedSlot(t)
	base := ExplainResolution(res)

	// 多次调用结果一致
	for i := 0; i < 10; i++ {
		if got := ExplainResolution(res); got != base {
			t.Fatalf("第 %d 次调用输出不一致:\n%s\n---\n%s", i, got, base)
		}
	}

	// 打乱槽位顺序后输出仍一致
	shuffled := &Resolution{Slots: make([]*Slot, len(res.Slots))}
	for i, slot := range res.Slots {
		shuffled.Slots[len(res.Slots)-1-i] = slot
	}
	if got := ExplainResolution(shuffled); got != base {
		t.Fatalf("打乱槽位顺序后输出不一致:\n%s\n---\n%s", got, base)
	}
}
