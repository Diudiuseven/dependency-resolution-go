package resolver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// LockFormat 是锁定文件的格式标识。
const LockFormat = "depres-lock/v1"

// Lock 表示一次求解的固化结果。
// 为保证可复现性，锁定内容不包含任何随运行时刻变化的数据（如时间戳）。
type Lock struct {
	Format              string       `json:"format"`
	RegistryFingerprint string       `json:"registry_fingerprint"`
	Policy              string       `json:"policy"`
	Members             []string     `json:"members"`
	Slots               []LockedSlot `json:"slots"`
}

// removedLockFields 是历史上存在、本版已移除的锁定文件字段。
// 读取旧锁定文件时兼容处理（忽略），但会给出可区分的提示。
var removedLockFields = []string{"resolved_at"}

// LockedSlot 表示锁定的槽位。
type LockedSlot struct {
	Name      string       `json:"name"`
	Boundary  string       `json:"boundary"`
	Version   string       `json:"version"`
	Integrity string       `json:"integrity"`
	Edges     []LockedEdge `json:"edges"`
}

// LockedEdge 表示锁定的依赖边。
type LockedEdge struct {
	Name       string `json:"name"`
	Constraint string `json:"constraint"`
}

// WriteLock 将锁定结果写入文件（原子写入：先写临时文件再重命名）。
func WriteLock(path string, lock *Lock) error {
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化锁定文件失败: %v", err)
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("写入锁定文件失败: %v", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("重命名锁定文件失败: %v", err)
	}
	return nil
}

// ReadLock 从文件读取锁定结果。
// 旧版本锁定文件中的已移除字段会被静默忽略；需要兼容提示时请使用 ReadLockCompat。
func ReadLock(path string) (*Lock, error) {
	lock, _, err := ReadLockCompat(path)
	return lock, err
}

// ReadLockCompat 从文件读取锁定结果，并对旧版本锁定文件中已调整或移除的内容
// 给出可区分的兼容提示（warnings）。旧锁定文件仍可正常读取、校验和使用。
func ReadLockCompat(path string) (*Lock, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取锁定文件失败: %v", err)
	}
	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, nil, fmt.Errorf("解析锁定文件失败: %v", err)
	}
	if lock.Format != LockFormat {
		return nil, nil, fmt.Errorf("不支持的锁定文件格式: %s", lock.Format)
	}
	// 检测已移除的字段，给出可区分的兼容提示
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err == nil {
		var warnings []string
		for _, field := range removedLockFields {
			if _, ok := raw[field]; ok {
				warnings = append(warnings, fmt.Sprintf(
					"锁定文件包含已移除的字段 %q，已兼容忽略（该字段不再写入，不影响校验与使用）", field))
			}
		}
		return &lock, warnings, nil
	}
	return &lock, nil, nil
}

// LockFromResolution 从求解结果生成锁定文件内容。
// 输出是确定性的: 相同求解结果、registry 与成员列表必产生逐字节一致的锁定内容，
// 不随运行时刻、机器、进程重启或并发而变化。
func LockFromResolution(r *Resolution, registry Registry, policy CompatibilityPolicy, members []string) (*Lock, error) {
	lock := &Lock{
		Format:              LockFormat,
		RegistryFingerprint: registry.Fingerprint(),
		Policy:              policy.Name(),
	}
	// 排序成员（复制后再排序，避免修改调用方的切片）
	lock.Members = make([]string, len(members))
	copy(lock.Members, members)
	sort.Strings(lock.Members)

	// 复制并按 (名称, 边界) 排序，保证写入顺序与 Resolution 内部槽位顺序无关
	slots := make([]*Slot, 0, len(r.Slots))
	for _, slot := range r.Slots {
		if slot.Selected == nil {
			continue // 未选定版本的槽位不进入锁定结果
		}
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].Key.Name != slots[j].Key.Name {
			return slots[i].Key.Name < slots[j].Key.Name
		}
		return slots[i].Key.Boundary < slots[j].Key.Boundary
	})

	for _, slot := range slots {
		ls := LockedSlot{
			Name:     slot.Key.Name,
			Boundary: slot.Key.Boundary,
			Version:  slot.Selected.String(),
		}
		// 计算完整性哈希
		integrity, err := computeIntegrity(registry, slot.Key.Name, *slot.Selected)
		if err != nil {
			return nil, err
		}
		ls.Integrity = integrity
		// 收集该槽位的依赖边（从 registry 清单中获取）
		manifest, err := registry.Manifest(slot.Key.Name, *slot.Selected)
		if err == nil {
			var edgeNames []string
			for n := range manifest.Dependencies {
				edgeNames = append(edgeNames, n)
			}
			sort.Strings(edgeNames)
			for _, n := range edgeNames {
				ls.Edges = append(ls.Edges, LockedEdge{
					Name:       n,
					Constraint: manifest.Dependencies[n],
				})
			}
		}
		lock.Slots = append(lock.Slots, ls)
	}
	return lock, nil
}

// computeIntegrity 计算包版本的完整性哈希。
func computeIntegrity(registry Registry, name string, v Version) (string, error) {
	manifest, err := registry.Manifest(name, v)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:]), nil
}

// FindLockedSlot 在锁定结果中查找槽位。
func FindLockedSlot(lock *Lock, name, boundary string) *LockedSlot {
	for i := range lock.Slots {
		if lock.Slots[i].Name == name && lock.Slots[i].Boundary == boundary {
			return &lock.Slots[i]
		}
	}
	return nil
}

// FindLockedSlotsByName 在锁定结果中按名称查找所有槽位。
func FindLockedSlotsByName(lock *Lock, name string) []*LockedSlot {
	var result []*LockedSlot
	for i := range lock.Slots {
		if lock.Slots[i].Name == name {
			result = append(result, &lock.Slots[i])
		}
	}
	return result
}
