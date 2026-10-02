package resolver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// LockFormat 是锁定文件的格式标识。
const LockFormat = "depres-lock/v1"

// Lock 表示一次求解的固化结果。
type Lock struct {
	Format              string       `json:"format"`
	ResolvedAt          string       `json:"resolved_at"`
	RegistryFingerprint string       `json:"registry_fingerprint"`
	Policy              string       `json:"policy"`
	Members             []string     `json:"members"`
	Slots               []LockedSlot `json:"slots"`
}

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
func ReadLock(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取锁定文件失败: %v", err)
	}
	var lock Lock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, fmt.Errorf("解析锁定文件失败: %v", err)
	}
	if lock.Format != LockFormat {
		return nil, fmt.Errorf("不支持的锁定文件格式: %s", lock.Format)
	}
	return &lock, nil
}

// LockFromResolution 从求解结果生成锁定文件内容。
func LockFromResolution(r *Resolution, registry Registry, policy CompatibilityPolicy, members []string) (*Lock, error) {
	lock := &Lock{
		Format:              LockFormat,
		ResolvedAt:          time.Now().UTC().Format(time.RFC3339),
		RegistryFingerprint: registry.Fingerprint(),
		Policy:              policy.Name(),
		Members:             members,
	}
	// 排序成员
	sort.Strings(lock.Members)

	for _, slot := range r.Slots {
		if slot.Selected == nil {
			continue
		}
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
