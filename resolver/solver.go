package resolver

import (
	"context"
	"sort"
)

// SlotKey 表示一个求解槽位的键。
type SlotKey struct {
	Name     string
	Boundary string
}

// SlotState 表示一个求解槽位的内部状态。
type SlotState struct {
	key         SlotKey
	selected    *Version
	constraints []ConstraintEdge
	edges       []DependencyEdge
}

// Slot 表示求解结果中的槽位（对外只读）。
type Slot struct {
	Key         SlotKey
	Selected    *Version
	Constraints []ConstraintEdge
}

// ConstraintEdge 表示一条施加到槽位上的约束边。
type ConstraintEdge struct {
	Constraint Constraint
	Source     Source
}

// Resolution 表示求解结果。
type Resolution struct {
	Slots []*Slot
}

// trailEntry 表示一条回溯记录。
type trailEntry struct {
	undo func()
}

// trail 管理回溯状态。
type trail struct {
	entries []trailEntry
}

func (t *trail) mark() int { return len(t.entries) }

func (t *trail) undoTo(mark int) {
	for i := len(t.entries) - 1; i >= mark; i-- {
		t.entries[i].undo()
	}
	t.entries = t.entries[:mark]
}

func (t *trail) record(undo func()) {
	t.entries = append(t.entries, trailEntry{undo: undo})
}

// Solver 是确定性依赖求解器。
type Solver struct {
	registry Registry
	policy   CompatibilityPolicy
	slots    map[SlotKey]*SlotState
	trail    trail
	active   []SlotKey
}

// NewSolver 创建求解器。
func NewSolver(registry Registry, policy CompatibilityPolicy) *Solver {
	return &Solver{
		registry: registry,
		policy:   policy,
		slots:    make(map[SlotKey]*SlotState),
	}
}

// Solve 根据根依赖边求解依赖图。
func (s *Solver) Solve(roots []DependencyEdge) (*Resolution, error) {
	return s.SolveContext(context.Background(), roots)
}

// SolveContext 带取消控制的求解。
func (s *Solver) SolveContext(ctx context.Context, roots []DependencyEdge) (*Resolution, error) {
	if err := s.solveRoots(ctx, roots); err != nil {
		return nil, err
	}
	return s.resolution(), nil
}

// solveRoots 处理一组依赖边，按确定性顺序求解。
func (s *Solver) solveRoots(ctx context.Context, edges []DependencyEdge) error {
	sorted := sortEdges(edges)
	for _, e := range sorted {
		if err := ctx.Err(); err != nil {
			return &ResolutionError{ErrKind: ErrCancelled, Message: "求解已取消", Cause: err}
		}
		if err := s.processEdge(ctx, e); err != nil {
			return err
		}
	}
	return nil
}

// processEdge 处理一条依赖边，将其求解到一个槽位。
func (s *Solver) processEdge(ctx context.Context, e DependencyEdge) error {
	c, err := ParseConstraint(e.Constraint)
	if err != nil {
		return &InvalidRangeError{Range: e.Constraint, Source: e.Source}
	}
	// 检查依赖是否存在
	allVersions, err := s.registry.Versions(e.Name)
	if err != nil {
		return err
	}
	if len(allVersions) == 0 {
		return &MissingDepError{Name: e.Name, Source: e.Source}
	}
	candidates, err := s.candidateBoundaries(e.Name, c)
	if err != nil {
		return err
	}
	if candidates == nil {
		// 收集该包所有已有槽位的约束，用于冲突说明
		seen := make(map[string]bool)
		var allConstraints []ConstraintSource
		addConstraint := func(cs ConstraintSource) {
			key := cs.Constraint.String() + "|" + cs.Source.String()
			if !seen[key] {
				seen[key] = true
				allConstraints = append(allConstraints, cs)
			}
		}
		addConstraint(ConstraintSource{Constraint: c, Source: e.Source})
		for _, slot := range s.slots {
			if slot.key.Name != e.Name {
				continue
			}
			for _, ce := range slot.constraints {
				addConstraint(ConstraintSource{Constraint: ce.Constraint, Source: ce.Source})
			}
		}
		return &ConflictError{
			Package:     e.Name,
			Constraints: allConstraints,
			Message:     "没有可用版本满足约束",
		}
	}

	// 收集所有尝试过的约束，用于冲突说明（去重）
	seen := make(map[string]bool)
	var allConstraints []ConstraintSource
	addConstraint := func(cs ConstraintSource) {
		key := cs.Constraint.String() + "|" + cs.Source.String()
		if !seen[key] {
			seen[key] = true
			allConstraints = append(allConstraints, cs)
		}
	}
	addConstraint(ConstraintSource{Constraint: c, Source: e.Source})

	for _, cand := range candidates {
		slot := s.getOrCreateSlot(e.Name, cand.boundary)
		mainMarker := s.trail.mark()

		// 添加约束（跨版本尝试保持）
		slot.constraints = append(slot.constraints, ConstraintEdge{Constraint: c, Source: e.Source})
		s.trail.record(func() {
			slot.constraints = slot.constraints[:len(slot.constraints)-1]
		})

		// 环检测：即使槽位已有满足版本，也要检查是否在当前求解路径中
		if s.isActive(slot.key) {
			s.trail.undoTo(mainMarker)
			return &CycleError{Path: s.cyclePath(slot.key)}
		}

		if slot.selected != nil && c.Satisfies(*slot.selected) {
			return nil
		}

		// 获取满足所有约束的版本（降序）
		versions := s.versionsSatisfying(slot)
		if len(versions) == 0 {
			for _, ce := range slot.constraints {
				addConstraint(ConstraintSource{Constraint: ce.Constraint, Source: ce.Source})
			}
			s.trail.undoTo(mainMarker)
			continue
		}

		for _, v := range versions {
			if slot.selected != nil && slot.selected.Equal(v) {
				return nil
			}
			if s.isActive(slot.key) {
				s.trail.undoTo(mainMarker)
				return &CycleError{Path: s.cyclePath(slot.key)}
			}

			versionMarker := s.trail.mark()

			// 选择版本
			prev := slot.selected
			slot.selected = &v
			s.trail.record(func() { slot.selected = prev })

			// 从清单构建依赖边
			manifest, err := s.registry.Manifest(e.Name, v)
			if err != nil {
				s.trail.undoTo(versionMarker)
				continue
			}
			newEdges := s.edgesFromManifest(manifest, slot.key)
			oldEdges := slot.edges
			slot.edges = newEdges
			s.trail.record(func() { slot.edges = oldEdges })

			// 递归求解传递依赖
			s.active = append(s.active, slot.key)
			if err := s.solveRoots(ctx, newEdges); err != nil {
				s.active = s.active[:len(s.active)-1]
				s.trail.undoTo(versionMarker)
				// 只有冲突错误才回溯，其他错误（环、缺失、非法范围、取消）立即返回
				if _, ok := err.(*ConflictError); !ok {
					s.trail.undoTo(mainMarker)
					return err
				}
				continue
			}
			s.active = s.active[:len(s.active)-1]
			return nil
		}

		s.trail.undoTo(mainMarker)
	}

	return &ConflictError{
		Package:     e.Name,
		Constraints: allConstraints,
		Message:     "在所有兼容边界中均未找到满足全部约束的版本",
	}
}

// boundaryCandidate 表示一个候选兼容边界及其版本列表。
type boundaryCandidate struct {
	boundary string
	versions []Version
}

// candidateBoundaries 返回满足约束的候选边界，按最高版本降序排列。
func (s *Solver) candidateBoundaries(name string, c Constraint) ([]boundaryCandidate, error) {
	versions, err := s.registry.Versions(name)
	if err != nil {
		return nil, err
	}
	var satisfying []Version
	for _, v := range versions {
		if c.Satisfies(v) {
			satisfying = append(satisfying, v)
		}
	}
	if len(satisfying) == 0 {
		return nil, nil
	}
	groups := make(map[string][]Version)
	for _, v := range satisfying {
		b := s.policy.Boundary(v)
		groups[b] = append(groups[b], v)
	}
	var candidates []boundaryCandidate
	for b, vs := range groups {
		sort.Slice(vs, func(i, j int) bool { return vs[i].Less(vs[j]) })
		candidates = append(candidates, boundaryCandidate{boundary: b, versions: vs})
	}
	sort.Slice(candidates, func(i, j int) bool {
		vi := candidates[i].versions
		vj := candidates[j].versions
		return vi[len(vi)-1].Compare(vj[len(vj)-1]) > 0
	})
	return candidates, nil
}

// versionsSatisfying 返回槽位中满足所有约束的版本，降序排列。
func (s *Solver) versionsSatisfying(slot *SlotState) []Version {
	versions, err := s.registry.Versions(slot.key.Name)
	if err != nil {
		return nil
	}
	var candidates []Version
	for _, v := range versions {
		if s.policy.Boundary(v) != slot.key.Boundary {
			continue
		}
		ok := true
		for _, ce := range slot.constraints {
			if !ce.Constraint.Satisfies(v) {
				ok = false
				break
			}
		}
		if ok {
			candidates = append(candidates, v)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[j].Less(candidates[i]) })
	return candidates
}

// getOrCreateSlot 获取或创建槽位。
func (s *Solver) getOrCreateSlot(name, boundary string) *SlotState {
	key := SlotKey{Name: name, Boundary: boundary}
	if slot, ok := s.slots[key]; ok {
		return slot
	}
	slot := &SlotState{key: key}
	s.slots[key] = slot
	return slot
}

// isActive 检查槽位是否在当前求解路径中（用于环检测）。
func (s *Solver) isActive(key SlotKey) bool {
	for _, k := range s.active {
		if k == key {
			return true
		}
	}
	return false
}

// cyclePath 返回环路径。
func (s *Solver) cyclePath(key SlotKey) []string {
	path := []string{key.Name}
	for i := len(s.active) - 1; i >= 0; i-- {
		path = append([]string{s.active[i].Name}, path...)
		if s.active[i] == key {
			break
		}
	}
	return path
}

// edgesFromManifest 从包清单构建依赖边。
func (s *Solver) edgesFromManifest(m *PackageManifest, from SlotKey) []DependencyEdge {
	var names []string
	for n := range m.Dependencies {
		names = append(names, n)
	}
	sort.Strings(names)
	edges := make([]DependencyEdge, 0, len(names))
	for _, n := range names {
		edges = append(edges, DependencyEdge{
			Source:     Source{Kind: "package", Name: m.Name + "@" + m.Version},
			Name:       n,
			Constraint: m.Dependencies[n],
		})
	}
	return edges
}

// resolution 生成求解结果。
func (s *Solver) resolution() *Resolution {
	var keys []SlotKey
	for k := range s.slots {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Name != keys[j].Name {
			return keys[i].Name < keys[j].Name
		}
		return keys[i].Boundary < keys[j].Boundary
	})
	slots := make([]*Slot, 0, len(keys))
	for _, k := range keys {
		st := s.slots[k]
		slots = append(slots, &Slot{
			Key:         k,
			Selected:    st.selected,
			Constraints: st.constraints,
		})
	}
	return &Resolution{Slots: slots}
}

// sortEdges 按确定性顺序排序依赖边。
func sortEdges(edges []DependencyEdge) []DependencyEdge {
	sorted := make([]DependencyEdge, len(edges))
	copy(sorted, edges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Constraint < sorted[j].Constraint
	})
	return sorted
}
