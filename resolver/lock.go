package resolver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// LockFormat 是当前锁定文件的格式标识。
const LockFormat = "depres-lock/v2"

// LockFormatV1 是历史版本（含 resolved_at 时间戳，不含未选定槽位）的格式标识。
// 读取该格式时会做兼容转换并给出可区分的提示。
const LockFormatV1 = "depres-lock/v1"

// CompatNoticeKind 表示读取旧锁定结果时的兼容提示类别。
type CompatNoticeKind string

const (
	// CompatNoticeV1Format 文件整体是旧的 depres-lock/v1 格式。
	CompatNoticeV1Format CompatNoticeKind = "legacy-v1-format"
	// CompatNoticeResolvedAtRemoved 文件携带本次已移除的 resolved_at 字段（时间戳）。
	CompatNoticeResolvedAtRemoved CompatNoticeKind = "resolved-at-removed"
	// CompatNoticeUnknownField 文件携带当前格式不认识的字段。
	CompatNoticeUnknownField CompatNoticeKind = "unknown-field"
)

// CompatNotice 是读取旧/异构锁定结果时的可区分兼容提示。
type CompatNotice struct {
	Kind    CompatNoticeKind
	Field   string // 相关字段名（如适用）
	Message string
}

// Lock 表示一次求解的固化结果。
//
// 可复现性保证：Lock 不含任何运行时刻、机器或进程相关的信息，
// 相同的求解结论、registry 指纹、策略与成员下，序列化结果逐字节一致。
type Lock struct {
	Format              string           `json:"format"`
	RegistryFingerprint string           `json:"registry_fingerprint"`
	Policy              string           `json:"policy"`
	Members             []string         `json:"members"`
	Slots               []LockedSlot     `json:"slots"`
	UnresolvedSlots     []UnresolvedSlot `json:"unresolved_slots"`
}

// LockedSlot 表示锁定的、已选定具体版本的槽位。
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

// UnresolvedSlot 表示求解结果中最终没有落到具体版本的槽位，
// 必须如实固化，不得跳过。
type UnresolvedSlot struct {
	Name     string `json:"name"`
	Boundary string `json:"boundary"`
}

// lockWire 是确定性的序列化结构：所有切片字段都保证以 [] 而非 null 输出。
type lockWire struct {
	Format              string           `json:"format"`
	RegistryFingerprint string           `json:"registry_fingerprint"`
	Policy              string           `json:"policy"`
	Members             []string         `json:"members"`
	Slots               []LockedSlot     `json:"slots"`
	UnresolvedSlots     []UnresolvedSlot `json:"unresolved_slots"`
}

// lockedSlotWire 保证 edges 始终输出为数组。
type lockedSlotWire struct {
	Name      string       `json:"name"`
	Boundary  string       `json:"boundary"`
	Version   string       `json:"version"`
	Integrity string       `json:"integrity"`
	Edges     []LockedEdge `json:"edges"`
}

// MarshalJSON 确定性序列化：归一化所有 nil 切片为空切片。
func (l Lock) MarshalJSON() ([]byte, error) {
	w := lockWire{
		Format:              l.Format,
		RegistryFingerprint: l.RegistryFingerprint,
		Policy:              l.Policy,
		Members:             l.Members,
		Slots:               make([]LockedSlot, 0, len(l.Slots)),
		UnresolvedSlots:     make([]UnresolvedSlot, 0, len(l.UnresolvedSlots)),
	}
	if w.Members == nil {
		w.Members = []string{}
	}
	for _, s := range l.Slots {
		sw := lockedSlotWire{
			Name:      s.Name,
			Boundary:  s.Boundary,
			Version:   s.Version,
			Integrity: s.Integrity,
			Edges:     s.Edges,
		}
		if sw.Edges == nil {
			sw.Edges = []LockedEdge{}
		}
		w.Slots = append(w.Slots, LockedSlot{
			Name: s.Name, Boundary: s.Boundary, Version: s.Version, Integrity: s.Integrity,
			Edges: sw.Edges,
		})
	}
	w.UnresolvedSlots = append(w.UnresolvedSlots, l.UnresolvedSlots...)
	return json.Marshal(w)
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

// lockV1 是历史格式的最小描述，用于兼容读取。
type lockV1 struct {
	Format              string       `json:"format"`
	ResolvedAt          string       `json:"resolved_at"`
	RegistryFingerprint string       `json:"registry_fingerprint"`
	Policy              string       `json:"policy"`
	Members             []string     `json:"members"`
	Slots               []LockedSlot `json:"slots"`
}

// ReadLock 从文件读取锁定结果，兼容历史的 depres-lock/v1 格式。
// 兼容处理发生时的可区分提示通过 ReadLockWithNotices 获取。
func ReadLock(path string) (*Lock, error) {
	lock, _, err := ReadLockWithNotices(path)
	return lock, err
}

// ReadLockWithNotices 从文件读取锁定结果并返回兼容提示。
//
// 兼容策略：
//   - depres-lock/v2：按当前格式读取；其中无法识别的顶层字段以 unknown-field 提示报告。
//   - depres-lock/v1：转换为当前内存模型继续使用；给出 legacy-v1-format 提示，
//     其 resolved_at 时间戳被忽略并以 resolved-at-removed 提示单独标注；
//     旧格式无法表达的未选定槽位记为空集合。
//   - 其他格式：返回错误，不静默接受。
func ReadLockWithNotices(path string) (*Lock, []CompatNotice, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("读取锁定文件失败: %v", err)
	}
	return parseLock(data)
}

// parseLock 解析锁定文件字节并完成兼容转换，便于测试直接调用。
func parseLock(data []byte) (*Lock, []CompatNotice, error) {
	// 先以通用结构确定格式，同时收集未知字段。
	var head struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, nil, fmt.Errorf("解析锁定文件失败: %v", err)
	}
	switch head.Format {
	case LockFormat:
		return parseLockV2(data)
	case LockFormatV1:
		return parseLockV1(data)
	case "":
		return nil, nil, fmt.Errorf("不支持的锁定文件格式: %q（缺少 format 字段）", head.Format)
	default:
		return nil, nil, fmt.Errorf("不支持的锁定文件格式: %s", head.Format)
	}
}

func parseLockV2(data []byte) (*Lock, []CompatNotice, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("解析锁定文件失败: %v", err)
	}
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, nil, fmt.Errorf("解析锁定文件失败: %v", err)
	}
	// 归一化：旧写入方可能留下 null 切片或已移除字段。
	if l.Members == nil {
		l.Members = []string{}
	}
	if l.Slots == nil {
		l.Slots = []LockedSlot{}
	}
	if l.UnresolvedSlots == nil {
		l.UnresolvedSlots = []UnresolvedSlot{}
	}
	for i := range l.Slots {
		if l.Slots[i].Edges == nil {
			l.Slots[i].Edges = []LockedEdge{}
		}
	}

	knownV2Fields := map[string]bool{
		"format": true, "registry_fingerprint": true, "policy": true,
		"members": true, "slots": true, "unresolved_slots": true,
	}
	var notices []CompatNotice
	for field := range raw {
		if !knownV2Fields[field] {
			notices = append(notices, CompatNotice{
				Kind:    CompatNoticeUnknownField,
				Field:   field,
				Message: fmt.Sprintf("锁定文件包含当前格式不认识的字段 %q，已忽略", field),
			})
		}
	}
	sortNotices(notices)
	return &l, notices, nil
}

func parseLockV1(data []byte) (*Lock, []CompatNotice, error) {
	var old lockV1
	if err := json.Unmarshal(data, &old); err != nil {
		return nil, nil, fmt.Errorf("解析 v1 锁定文件失败: %v", err)
	}
	l := &Lock{
		Format:              LockFormatV1, // 保留原始格式标识，调用方可知其来源
		RegistryFingerprint: old.RegistryFingerprint,
		Policy:              old.Policy,
		Members:             old.Members,
		Slots:               old.Slots,
		UnresolvedSlots:     []UnresolvedSlot{},
	}
	if l.Members == nil {
		l.Members = []string{}
	}
	if l.Slots == nil {
		l.Slots = []LockedSlot{}
	}
	for i := range l.Slots {
		if l.Slots[i].Edges == nil {
			l.Slots[i].Edges = []LockedEdge{}
		}
	}
	notices := []CompatNotice{{
		Kind:    CompatNoticeV1Format,
		Field:   "format",
		Message: "锁定文件为旧格式 depres-lock/v1，已兼容读取；重新求解后建议写回为 depres-lock/v2",
	}}
	if old.ResolvedAt != "" {
		notices = append(notices, CompatNotice{
			Kind:    CompatNoticeResolvedAtRemoved,
			Field:   "resolved_at",
			Message: fmt.Sprintf("旧字段 resolved_at（%s）已移除且不再参与复现性，读取时已忽略", old.ResolvedAt),
		})
	}
	return l, notices, nil
}

// sortNotices 保证提示顺序确定（按字段名、类别排序）。
func sortNotices(notices []CompatNotice) {
	sort.Slice(notices, func(i, j int) bool {
		if notices[i].Field != notices[j].Field {
			return notices[i].Field < notices[j].Field
		}
		return notices[i].Kind < notices[j].Kind
	})
}

// LockFromResolution 从求解结果生成锁定内容。
//
// 输出不包含时间戳等随运行时刻变化的信息：已选定与未选定的槽位都如实固化，
// 顺序取自 Resolution 的确定性排序，因此相同输入逐字节一致。
func LockFromResolution(r *Resolution, registry Registry, policy CompatibilityPolicy, members []string) (*Lock, error) {
	lock := &Lock{
		Format:              LockFormat,
		RegistryFingerprint: registry.Fingerprint(),
		Policy:              policy.Name(),
		Members:             append([]string(nil), members...),
		Slots:               []LockedSlot{},
		UnresolvedSlots:     []UnresolvedSlot{},
	}
	sort.Strings(lock.Members)

	for _, slot := range r.Slots {
		if slot.Selected == nil {
			lock.UnresolvedSlots = append(lock.UnresolvedSlots, UnresolvedSlot{
				Name:     slot.Key.Name,
				Boundary: slot.Key.Boundary,
			})
			continue
		}
		ls := LockedSlot{
			Name:     slot.Key.Name,
			Boundary: slot.Key.Boundary,
			Version:  slot.Selected.String(),
			Edges:    []LockedEdge{},
		}
		integrity, err := computeIntegrity(registry, slot.Key.Name, *slot.Selected)
		if err != nil {
			return nil, err
		}
		ls.Integrity = integrity
		// 收集该槽位的依赖边（从 registry 清单中获取），按名称排序保证确定性
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
