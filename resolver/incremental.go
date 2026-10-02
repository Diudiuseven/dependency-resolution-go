package resolver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// IncrementalResolver 支持增量求解。
type IncrementalResolver struct {
	registry Registry
	policy   CompatibilityPolicy
	cache    map[string]cachedSlot
}

// cachedSlot 表示缓存的槽位求解结果。
type cachedSlot struct {
	Version Version
	Edges   []DependencyEdge
}

// NewIncrementalResolver 创建增量求解器。
func NewIncrementalResolver(registry Registry, policy CompatibilityPolicy) *IncrementalResolver {
	return &IncrementalResolver{
		registry: registry,
		policy:   policy,
		cache:    make(map[string]cachedSlot),
	}
}

// Resolve 全量求解。
func (r *IncrementalResolver) Resolve(roots []DependencyEdge) (*Resolution, error) {
	solver := NewSolver(r.registry, r.policy)
	return solver.Solve(roots)
}

// ResolveIncremental 增量求解：仅重算受影响部分。
// 增量结论与全量重算完全一致。
func (r *IncrementalResolver) ResolveIncremental(ctx context.Context, roots []DependencyEdge, prev *Resolution) (*Resolution, error) {
	// 构建前一次结果的缓存
	if prev != nil {
		r.populateCache(prev)
	}

	// 使用带缓存的求解器
	solver := &cachingSolver{
		registry: r.registry,
		policy:   r.policy,
		cache:    r.cache,
		slots:    make(map[SlotKey]*SlotState),
	}

	if err := solver.solveRoots(ctx, roots); err != nil {
		return nil, err
	}
	return solver.resolution(), nil
}

// populateCache 从求解结果填充缓存。
func (r *IncrementalResolver) populateCache(res *Resolution) {
	for _, slot := range res.Slots {
		if slot.Selected == nil {
			continue
		}
		key := cacheKey(slot.Key.Name, slot.Key.Boundary, slot.Constraints)
		// 从 registry 获取该版本的依赖边
		manifest, err := r.registry.Manifest(slot.Key.Name, *slot.Selected)
		if err != nil {
			continue
		}
		var edges []DependencyEdge
		for n, c := range manifest.Dependencies {
			edges = append(edges, DependencyEdge{Name: n, Constraint: c})
		}
		r.cache[key] = cachedSlot{
			Version: *slot.Selected,
			Edges:   edges,
		}
	}
}

// cacheKey 生成缓存键：名称 + 边界 + 排序后的约束集合。
func cacheKey(name, boundary string, constraints []ConstraintEdge) string {
	var parts []string
	for _, ce := range constraints {
		parts = append(parts, ce.Constraint.String())
	}
	sort.Strings(parts)
	h := sha256.New()
	h.Write([]byte(name))
	h.Write([]byte(boundary))
	h.Write([]byte(strings.Join(parts, ",")))
	return hex.EncodeToString(h.Sum(nil))
}

// cachingSolver 带缓存的求解器。
type cachingSolver struct {
	registry Registry
	policy   CompatibilityPolicy
	cache    map[string]cachedSlot
	slots    map[SlotKey]*SlotState
	trail    trail
	active   []SlotKey
}

// solveRoots 处理一组依赖边。
func (s *cachingSolver) solveRoots(ctx context.Context, edges []DependencyEdge) error {
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

// processEdge 处理一条依赖边，使用缓存加速。
func (s *cachingSolver) processEdge(ctx context.Context, e DependencyEdge) error {
	c, err := ParseConstraint(e.Constraint)
	if err != nil {
		return &InvalidRangeError{Range: e.Constraint, Source: e.Source}
	}
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
		return &ConflictError{
			Package:     e.Name,
			Constraints: []ConstraintSource{{Constraint: c, Source: e.Source}},
			Message:     "没有可用版本满足约束",
		}
	}

	for _, cand := range candidates {
		slot := s.getOrCreateSlot(e.Name, cand.boundary)
		mainMarker := s.trail.mark()

		slot.constraints = append(slot.constraints, ConstraintEdge{Constraint: c, Source: e.Source})
		s.trail.record(func() {
			slot.constraints = slot.constraints[:len(slot.constraints)-1]
		})

		if s.isActive(slot.key) {
			s.trail.undoTo(mainMarker)
			return &CycleError{Path: s.cyclePath(slot.key)}
		}

		if slot.selected != nil && c.Satisfies(*slot.selected) {
			return nil
		}

		versions := s.versionsSatisfying(slot)
		if len(versions) == 0 {
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

			// 尝试从缓存获取
			key := cacheKey(slot.key.Name, slot.key.Boundary, slot.constraints)
			if cached, ok := s.cache[key]; ok && cached.Version.Equal(v) {
				// 缓存命中：使用缓存的边
				prev := slot.selected
				slot.selected = &cached.Version
				s.trail.record(func() { slot.selected = prev })

				oldEdges := slot.edges
				slot.edges = cached.Edges
				s.trail.record(func() { slot.edges = oldEdges })

				s.active = append(s.active, slot.key)
				if err := s.solveRoots(ctx, cached.Edges); err != nil {
					s.active = s.active[:len(s.active)-1]
					s.trail.undoTo(versionMarker)
					if _, ok := err.(*ConflictError); !ok {
						s.trail.undoTo(mainMarker)
						return err
					}
					continue
				}
				s.active = s.active[:len(s.active)-1]
				return nil
			}

			// 缓存未命中：从 registry 获取清单
			prev := slot.selected
			slot.selected = &v
			s.trail.record(func() { slot.selected = prev })

			manifest, err := s.registry.Manifest(e.Name, v)
			if err != nil {
				s.trail.undoTo(versionMarker)
				continue
			}
			newEdges := s.edgesFromManifest(manifest)
			oldEdges := slot.edges
			slot.edges = newEdges
			s.trail.record(func() { slot.edges = oldEdges })

			// 更新缓存
			s.cache[key] = cachedSlot{Version: v, Edges: newEdges}

			s.active = append(s.active, slot.key)
			if err := s.solveRoots(ctx, newEdges); err != nil {
				s.active = s.active[:len(s.active)-1]
				s.trail.undoTo(versionMarker)
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
		Constraints: []ConstraintSource{{Constraint: c, Source: e.Source}},
		Message:     "在所有兼容边界中均未找到满足全部约束的版本",
	}
}

// candidateBoundaries 返回满足约束的候选边界。
func (s *cachingSolver) candidateBoundaries(name string, c Constraint) ([]boundaryCandidate, error) {
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

// versionsSatisfying 返回满足所有约束的版本。
func (s *cachingSolver) versionsSatisfying(slot *SlotState) []Version {
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
func (s *cachingSolver) getOrCreateSlot(name, boundary string) *SlotState {
	key := SlotKey{Name: name, Boundary: boundary}
	if slot, ok := s.slots[key]; ok {
		return slot
	}
	slot := &SlotState{key: key}
	s.slots[key] = slot
	return slot
}

// isActive 检查槽位是否在当前求解路径中。
func (s *cachingSolver) isActive(key SlotKey) bool {
	for _, k := range s.active {
		if k == key {
			return true
		}
	}
	return false
}

// cyclePath 返回环路径。
func (s *cachingSolver) cyclePath(key SlotKey) []string {
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
func (s *cachingSolver) edgesFromManifest(m *PackageManifest) []DependencyEdge {
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
func (s *cachingSolver) resolution() *Resolution {
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
