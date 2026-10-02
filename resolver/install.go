package resolver

import (
	"fmt"
	"path/filepath"
	"sort"
)

// InstallPolicy 表示安装/校验策略。
type InstallPolicy int

const (
	// PolicyStrict 严格模式：任何不一致都报错。
	PolicyStrict InstallPolicy = iota
	// PolicyWarn 警告模式：报告不一致但继续。
	PolicyWarn
	// PolicyUpdate 更新模式：重新求解并更新锁定文件。
	PolicyUpdate
)

// InstallPlan 表示安装计划。
type InstallPlan struct {
	Slots []InstallSlot
}

// InstallSlot 表示一个需要安装的槽位。
type InstallSlot struct {
	Name     string
	Boundary string
	Version  Version
	Path     string
}

// VerifyResult 表示校验结果。
type VerifyResult struct {
	Missing []MissingItem
	Drift   []DriftItem
}

// MissingItem 表示清单中有但锁定结果中缺失的项。
type MissingItem struct {
	Name       string
	Constraint string
	Source     Source
}

// DriftItem 表示锁定结果与 registry 不一致的项。
type DriftItem struct {
	Name     string
	Boundary string
	Expected Version
	Actual   Version
	Reason   string
}

// Install 根据锁定结果执行安装。
func Install(lock *Lock, registry Registry, targetDir string) (*InstallPlan, error) {
	// 校验 registry 指纹
	if registry.Fingerprint() != lock.RegistryFingerprint {
		return nil, fmt.Errorf("registry 指纹不匹配: 锁定 %s, 当前 %s", lock.RegistryFingerprint, registry.Fingerprint())
	}

	plan := &InstallPlan{}
	for _, ls := range lock.Slots {
		v, err := ParseVersion(ls.Version)
		if err != nil {
			return nil, fmt.Errorf("锁定文件中版本非法 %s@%s: %v", ls.Name, ls.Version, err)
		}
		// 验证版本在 registry 中存在
		manifest, err := registry.Manifest(ls.Name, v)
		if err != nil {
			return nil, fmt.Errorf("安装失败 %s@%s: %v", ls.Name, ls.Version, err)
		}
		// 验证完整性
		integrity, err := computeIntegrity(registry, ls.Name, v)
		if err != nil {
			return nil, err
		}
		if integrity != ls.Integrity {
			return nil, fmt.Errorf("完整性校验失败 %s@%s: 锁定 %s, 实际 %s", ls.Name, ls.Version, ls.Integrity, integrity)
		}
		_ = manifest
		plan.Slots = append(plan.Slots, InstallSlot{
			Name:     ls.Name,
			Boundary: ls.Boundary,
			Version:  v,
			Path:     filepath.Join(targetDir, ls.Name, ls.Version),
		})
	}
	// 按名称排序保证确定性
	sort.Slice(plan.Slots, func(i, j int) bool {
		if plan.Slots[i].Name != plan.Slots[j].Name {
			return plan.Slots[i].Name < plan.Slots[j].Name
		}
		return plan.Slots[i].Boundary < plan.Slots[j].Boundary
	})
	return plan, nil
}

// Verify 校验清单与锁定结果的一致性。
func Verify(manifests []*ModuleManifest, lock *Lock, registry Registry) (*VerifyResult, error) {
	result := &VerifyResult{}

	// 收集所有清单中的依赖边
	var allEdges []DependencyEdge
	for _, m := range manifests {
		source := Source{Kind: "workspace", Name: m.Name}
		allEdges = append(allEdges, m.Edges(source)...)
	}

	// 检查缺项：清单中的依赖边在锁定结果中没有满足的槽位
	for _, edge := range allEdges {
		c, err := ParseConstraint(edge.Constraint)
		if err != nil {
			return nil, &InvalidRangeError{Range: edge.Constraint, Source: edge.Source}
		}
		slots := FindLockedSlotsByName(lock, edge.Name)
		found := false
		for _, ls := range slots {
			v, err := ParseVersion(ls.Version)
			if err != nil {
				continue
			}
			if c.Satisfies(v) {
				found = true
				break
			}
		}
		if !found {
			result.Missing = append(result.Missing, MissingItem{
				Name:       edge.Name,
				Constraint: edge.Constraint,
				Source:     edge.Source,
			})
		}
	}

	// 检查漂移：锁定结果中的版本与 registry 不一致
	for _, ls := range lock.Slots {
		v, err := ParseVersion(ls.Version)
		if err != nil {
			continue
		}
		// 检查 registry 中是否还有该版本
		_, err = registry.Manifest(ls.Name, v)
		if err != nil {
			result.Drift = append(result.Drift, DriftItem{
				Name:     ls.Name,
				Boundary: ls.Boundary,
				Expected: v,
				Reason:   "registry 中不再存在该版本",
			})
			continue
		}
		// 检查完整性
		integrity, err := computeIntegrity(registry, ls.Name, v)
		if err != nil {
			continue
		}
		if integrity != ls.Integrity {
			result.Drift = append(result.Drift, DriftItem{
				Name:     ls.Name,
				Boundary: ls.Boundary,
				Expected: v,
				Reason:   "registry 内容已变更",
			})
		}
	}

	return result, nil
}

// HasIssues 返回校验结果是否有问题。
func (r *VerifyResult) HasIssues() bool {
	return len(r.Missing) > 0 || len(r.Drift) > 0
}

// Summary 返回校验结果的可读摘要。
func (r *VerifyResult) Summary() string {
	if !r.HasIssues() {
		return "校验通过：清单与锁定结果一致"
	}
	var msg string
	if len(r.Missing) > 0 {
		msg += fmt.Sprintf("缺项 %d 个:\n", len(r.Missing))
		for _, m := range r.Missing {
			msg += fmt.Sprintf("  - %s@%s (来源: %s)\n", m.Name, m.Constraint, m.Source.String())
		}
	}
	if len(r.Drift) > 0 {
		msg += fmt.Sprintf("漂移 %d 个:\n", len(r.Drift))
		for _, d := range r.Drift {
			msg += fmt.Sprintf("  - %s@%s (原因: %s)\n", d.Name, d.Expected.String(), d.Reason)
		}
	}
	return msg
}
