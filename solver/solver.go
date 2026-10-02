// Package solver turns a merged dependency graph plus a local registry into
// a deterministic selection of package versions. The search is an
// iterative backtracking DFS over "lanes" (package x major). Candidate
// versions are tried highest-first; lane choice order is deterministic
// (fewest remaining candidates, ties lexicographic). Every decision and
// rejection is recorded for explainability. Identical inputs always yield
// identical selections regardless of scheduling or process restarts.
package solver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Diudiuseven/dependency-resolution-go/compat"
	"github.com/Diudiuseven/dependency-resolution-go/graph"
	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// Selection is one chosen package version in the result.
type Selection struct {
	Package string
	Version semver.Version
	Lane    compat.Lane
	Reasons []manifest.Source
}

// Decision records why one lane was bound to one version.
type Decision struct {
	Package    string
	Lane       string
	Chosen     semver.Version
	Candidates []string
	Constraint string
	Rejected   []Rejection
	Reasons    []manifest.Source
}

// Rejection explains why a candidate version could not be selected.
type Rejection struct {
	Version string
	Reason  string
	Sources []manifest.Source
}

// Result is one successful solve.
type Result struct {
	Selections []Selection
	Decisions  []Decision
	InputsHash string
}

// SelectionFor returns the chosen version of package in lane, if any.
func (r *Result) SelectionFor(pkg, lane string) (Selection, bool) {
	for _, s := range r.Selections {
		if s.Package == pkg && s.Lane.Key() == lane {
			return s, true
		}
	}
	return Selection{}, false
}

// Options configures a Solve.
type Options struct {
	Compat        compat.Config
	MaxBacktracks int // 0 => default (10000); -1 => unlimited
	MaxSteps      int // 0 => default (200000)
}

// DefaultOptions returns deterministic default options.
func DefaultOptions() Options {
	return Options{Compat: compat.DefaultConfig(), MaxBacktracks: 10000, MaxSteps: 200000}
}

// Solver runs deterministic resolutions.
type Solver struct {
	reg registry.Registry
	opt Options
}

// New creates a Solver backed by a local registry.
func New(reg registry.Registry, opt Options) *Solver {
	if opt.Compat.Rule == nil {
		opt.Compat = compat.DefaultConfig()
	}
	if opt.MaxBacktracks == 0 {
		opt.MaxBacktracks = 10000
	}
	if opt.MaxSteps == 0 {
		opt.MaxSteps = 200000
	}
	return &Solver{reg: reg, opt: opt}
}

// ---- lanes -----------------------------------------------------------

// declEntry is one declaration plus the depth at which it was introduced.
type laneColor int

const (
	laneWhite laneColor = iota // not yet part of the committed path
	laneGray                   // on the active recursion path
	laneBlack                  // fully solved (a committed decision)
)

type laneState struct {
	lane     compat.Lane
	id       string
	versions semver.VersionList

	// decls are all declarations currently constraining this lane. Snapshot
	// / restore handles backtracking, so no per-depth tagging is needed.
	decls []graph.Declaration

	// createdDepth is the depth that created this lane (-1 for seeds).
	createdDepth int

	color    laneColor
	chosen   semver.Version
	bound    bool
	applied  map[string]bool
	tried    map[string]bool
	decision Decision
}

func newLaneState(l compat.Lane, vs semver.VersionList) *laneState {
	return &laneState{lane: l, id: l.Package + "#" + l.Key(),
		versions: vs,
		applied:  map[string]bool{}, tried: map[string]bool{}}
}

func (st *laneState) key() string { return st.id }

func (st *laneState) satisfiesAll(v semver.Version) bool {
	for _, d := range st.decls {
		if !d.Range.Contains(v) {
			return false
		}
	}
	return true
}

func (st *laneState) addDecl(d graph.Declaration) { st.decls = append(st.decls, d) }

type search struct {
	s   *Solver
	g   *graph.Graph
	ctx context.Context

	lanes map[string]*laneState
	pkgLs map[string]map[string]*laneState
	order []*laneState

	// path is the DFS recursion stack of lanes (the gray chain).
	path []*laneState

	versionsCache map[string]semver.VersionList
	releaseCache  map[string]registry.Release
	seedLanes     []*laneState
	reach         map[string]bool

	steps      int
	backtracks int
}

func newSearch(s *Solver, g *graph.Graph, ctx context.Context) *search {
	return &search{
		s:             s,
		g:             g,
		ctx:           ctx,
		lanes:         map[string]*laneState{},
		pkgLs:         map[string]map[string]*laneState{},
		versionsCache: map[string]semver.VersionList{},
		releaseCache:  map[string]registry.Release{},
	}
}

func (se *search) depth() int { return len(se.path) - 1 }

func (se *search) lane(pkg, lk string) *laneState {
	return se.lanes[pkg+"#"+lk]
}

func (se *search) versions(pkg string) (semver.VersionList, error) {
	if vs, ok := se.versionsCache[pkg]; ok {
		return vs, nil
	}
	vs, err := se.s.reg.Versions(se.ctx, pkg)
	if err != nil {
		return nil, err
	}
	vs = vs.Sorted()
	se.versionsCache[pkg] = vs
	return vs, nil
}

func (se *search) release(pkg string, v semver.Version) (registry.Release, error) {
	key := pkg + "@" + v.String()
	if rel, ok := se.releaseCache[key]; ok {
		return rel, nil
	}
	rel, err := se.s.reg.Release(se.ctx, pkg, v)
	if err != nil {
		return registry.Release{}, err
	}
	se.releaseCache[key] = rel
	return rel, nil
}

// ensureLane materializes a lane.
func (se *search) ensureLane(l compat.Lane) (*laneState, bool) {
	k := l.Package + "#" + l.Key()
	if st := se.lanes[k]; st != nil {
		return st, false
	}
	rule := se.s.opt.Compat.RuleFor(l.Package)
	st := newLaneState(l, filterLaneVersions(l, se.versionsCache[l.Package], rule))
	se.lanes[k] = st
	if se.pkgLs[l.Package] == nil {
		se.pkgLs[l.Package] = map[string]*laneState{}
	}
	se.pkgLs[l.Package][l.Key()] = st
	se.order = append(se.order, st)
	return st, true
}

// Solve resolves all packages reachable in g.
func (s *Solver) Solve(ctx context.Context, g *graph.Graph) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, canceledFailure(err)
	}
	if cycles := g.Cycles(); len(cycles) > 0 {
		return nil, cycleFailure(cycles)
	}
	se := newSearch(s, g, ctx)
	if err := se.seedRoots(); err != nil {
		return nil, err
	}
	se.seedLanes = append([]*laneState(nil), se.order...)
	if err := se.solveLanes(); err != nil {
		return nil, err
	}
	if cyc, err := se.detectSelectedCycle(); err != nil {
		return nil, err
	} else if cyc != nil {
		return nil, cyc
	}
	return se.buildResult(), nil
}

// detectSelectedCycle runs a deterministic three-color DFS over the concrete
// graph formed by every selected release (package -> dependency packages).
// This catches cycles that only appear through transitive release metadata
// even though the manifest-level graph was acyclic. Lanes are collapsed by
// package name because a cycle is a cycle regardless of which lane closes it.
func (se *search) detectSelectedCycle() (*Failure, error) {
	adj := map[string][]string{}
	keys := make([]string, 0, len(se.lanes))
	for k, st := range se.lanes {
		if !st.bound {
			continue
		}
		keys = append(keys, k)
		rel, err := se.release(st.lane.Package, st.chosen)
		if err != nil {
			return nil, err
		}
		set := map[string]bool{}
		for _, d := range rel.Deps {
			set[d.Name] = true
		}
		ns := make([]string, 0, len(set))
		for n := range set {
			ns = append(ns, n)
		}
		sort.Strings(ns)
		adj[st.lane.Package] = ns
	}
	sort.Strings(keys)
	color := map[string]int{} // 0 white, 1 gray, 2 black
	var stack []string
	var dfs func(u string) []string
	dfs = func(u string) []string {
		color[u] = 1
		stack = append(stack, u)
		for _, v := range adj[u] {
			if color[v] == 1 {
				var cyc []string
				for _, x := range stack {
					cyc = append(cyc, x)
					if x == v && len(cyc) > 1 {
						break
					}
				}
				cyc = append(cyc, v)
				return cyc
			}
			if color[v] == 0 {
				if c := dfs(v); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[u] = 2
		return nil
	}
	// Root packages first, then every known package in sorted order.
	roots := append([]string(nil), se.g.Roots...)
	for _, k := range keys {
		pkg := se.lanes[k].lane.Package
		roots = append(roots, pkg)
	}
	sort.Strings(roots)
	seen := map[string]bool{}
	for _, u := range roots {
		if seen[u] {
			continue
		}
		seen[u] = true
		if color[u] != 0 {
			continue
		}
		stack = stack[:0]
		if cyc := dfs(u); cyc != nil {
			return cycleFailureFromPath(cyc), nil
		}
	}
	return nil, nil
}

// seedRoots applies root declarations at depth -1.
func (se *search) seedRoots() error {
	for _, name := range se.g.Roots {
		node, ok := se.g.Node(name)
		if !ok {
			continue
		}
		for _, e := range node.Edges {
			if _, err := se.versions(e.To); err != nil {
				if _, ok := err.(*registry.ErrPackageMissing); ok {
					return missingPackageFailure(e.To, e.Decls)
				}
				return err
			}
			if err := se.routeDecls(e.To, e.Decls); err != nil {
				return err
			}
		}
	}
	return nil
}

// routeDecls attaches declarations to lanes at depth.
func (se *search) routeDecls(pkg string, decls []graph.Declaration) error {
	vs, err := se.versions(pkg)
	if err != nil {
		if _, ok := err.(*registry.ErrPackageMissing); ok {
			return missingPackageFailure(pkg, decls)
		}
		return err
	}
	rule := se.s.opt.Compat.RuleFor(pkg)
	allLanes := compat.SplitMajors(pkg, vs, se.s.opt.Compat)
	for _, d := range decls {
		if majors, pinned := d.Range.Majors(); pinned {
			if err := se.routePinned(pkg, d, majors, allLanes, vs, rule); err != nil {
				return err
			}
		}
	}
	for _, d := range decls {
		if _, pinned := d.Range.Majors(); pinned {
			continue
		}
		if err := se.routeOpen(pkg, d, allLanes, vs, rule); err != nil {
			return err
		}
	}
	return nil
}

func (se *search) routePinned(pkg string, d graph.Declaration, majors []uint64,
	allLanes []compat.Lane, vs semver.VersionList, rule compat.Rule) error {
	for _, major := range majors {
		var target *laneState
		for i := range allLanes {
			l := allLanes[i]
			if l.Major == major && laneIntersects(l, d.Range, vs, rule) {
				target, _ = se.ensureLane(l)
				break
			}
		}
		if target == nil {
			l := compat.NewLane(pkg, major, 0, rule)
			target, _ = se.ensureLane(l)
		}
		target.addDecl(d)
	}
	return nil
}

func (se *search) routeOpen(pkg string, d graph.Declaration, allLanes []compat.Lane,
	vs semver.VersionList, rule compat.Rule) error {
	// Open decls attach to every existing intersecting lane.
	var matched []*laneState
	for i := range allLanes {
		l := allLanes[i]
		if ex := se.lane(pkg, l.Key()); ex != nil && laneIntersects(l, d.Range, vs, rule) {
			matched = append(matched, ex)
		}
	}
	if len(matched) > 0 {
		for _, st := range matched {
			st.addDecl(d)
		}
		return nil
	}
	// Otherwise create the highest intersecting lane.
	for i := len(allLanes) - 1; i >= 0; i-- {
		l := allLanes[i]
		if !laneIntersects(l, d.Range, vs, rule) {
			continue
		}
		st, _ := se.ensureLane(l)
		st.addDecl(d)
		return nil
	}
	return missingPackageFailure(pkg, []graph.Declaration{d})
}

func laneIntersects(l compat.Lane, r semver.Range, vs semver.VersionList, rule compat.Rule) bool {
	for _, v := range vs {
		if rule.LaneKey(v.Major, breakingMinor(v, rule)) == l.Key() && r.Contains(v) {
			return true
		}
	}
	return false
}

func breakingMinor(v semver.Version, rule compat.Rule) uint64 {
	if sm, ok := rule.(compat.StrictMajor); ok && sm.TreatZeroMajorAsBreaking && v.Major == 0 {
		return v.Minor
	}
	return 0
}

func laneHasAny(st *laneState) bool {
	for _, v := range st.versions {
		if st.satisfiesAll(v) {
			return true
		}
	}
	return false
}

func satisfiedByAll(st *laneState, v semver.Version) bool { return st.satisfiesAll(v) }

// intersectionFailure builds a KindConflict failure for an infeasible lane.
func intersectionFailure(st *laneState) *Failure {
	return intersectionFailureUpTo(st, 0)
}

func filterLaneVersions(l compat.Lane, vs semver.VersionList, rule compat.Rule) semver.VersionList {
	out := make(semver.VersionList, 0, len(vs))
	for _, v := range vs {
		if rule.LaneKey(v.Major, breakingMinor(v, rule)) == l.Key() {
			out = append(out, v)
		}
	}
	return out.Sorted()
}

// ---- backtracking DFS ------------------------------------------------
//
// Recursive backtracking with per-lane candidate iteration and explicit
// state snapshots. The call stack IS the dependency path, so cycle
// detection is a simple "dependency reaches a lane already on the call
// stack" test. Diamond-shared dependencies are lanes committed earlier by a
// DIFFERENT root/subtree; they are black and are reused, not revisited.

// solveLanes binds every seed lane. Each independent seed subtree is solved
// on its own recursion path; committed lanes turn black and are reused.
func (se *search) solveLanes() error {
	for {
		if err := se.ctx.Err(); err != nil {
			return canceledFailure(err)
		}
		st := se.pickSeedLane()
		if st == nil {
			return nil
		}
		if err := se.bind(st, nil); err != nil {
			return err
		}
	}
}

// pickSeedLane returns the most-constrained unbound SEED lane (a lane that
// existed before solving started). Transitive lanes are handled inside bind.
func (se *search) pickSeedLane() *laneState {
	var best *laneState
	bestCount := 0
	for _, st := range se.seedLanes {
		if st.bound {
			continue
		}
		count := 0
		for _, v := range st.versions {
			if st.satisfiesAll(v) {
				count++
			}
		}
		if best == nil || count < bestCount ||
			(count == bestCount && laneLess(st, best)) {
			best, bestCount = st, count
		}
	}
	return best
}

// bind tries candidate versions of st (highest first) until st and all the
// lanes its release pulls in are consistently bound.
func (se *search) bind(st *laneState, ancestors []*laneState) error {
	se.steps++
	if se.s.opt.MaxSteps > 0 && se.steps > se.s.opt.MaxSteps {
		return &Failure{Kind: KindExhausted,
			Message: fmt.Sprintf("solver step budget (%d) exhausted", se.s.opt.MaxSteps)}
	}
	path := append(append([]*laneState{}, ancestors...), st)
	if len(ancestors) == 0 {
		se.reach = map[string]bool{}
	}
	se.reach[st.key()] = true
	var lastConflict *Failure
	for _, v := range se.feasible(st, ancestors) {
		if err := se.ctx.Err(); err != nil {
			return canceledFailure(err)
		}
		if st.tried[v.String()] {
			continue
		}
		st.tried[v.String()] = true

		snap := se.snapshot()
		st.chosen, st.bound, st.color = v, true, laneGray
		st.applied = map[string]bool{}
		st.decision = Decision{
			Package:    st.lane.Package,
			Lane:       st.lane.Key(),
			Chosen:     v,
			Candidates: versionsDesc(st.versions),
			Constraint: intersectionString(st.decls),
			Reasons:    sourcesOf(st.decls),
		}
		conflict := se.propagate(st, path)
		if conflict == nil {
			se.extendReach(st)
			conflict = se.bindChildren(path)
		}
		if conflict == nil {
			st.color = laneBlack
			return nil
		}
		f, isF := conflict.(*Failure)
		if !isF || (f.Kind != KindConflict && f.Kind != KindMissingPackage && f.Kind != KindCycle) {
			return conflict
		}
		st.decision.Rejected = append(st.decision.Rejected, Rejection{
			Version: v.String(), Reason: "leads to " + firstLine(f.Message)})
		lastConflict = f
		se.restore(snap, st)
	}
	if lastConflict != nil {
		return lastConflict
	}
	return intersectionFailure(st)
}

// bindChildren binds the unbound lanes reachable from the current path,
// most-constrained-first. Reachability follows selected-release dependency
// edges, so a sibling root lane that merely exists is never pulled in.
func (se *search) bindChildren(path []*laneState) error {
	for {
		st := se.pickReachable(path)
		if st == nil {
			return nil
		}
		if err := se.bind(st, path); err != nil {
			return err
		}
	}
}

// pickReachable chooses the most-constrained unbound lane reachable from
// the lanes on the current path.
func (se *search) pickReachable(path []*laneState) *laneState {
	reachable := se.reach
	var best *laneState
	bestCount := 0
	for _, st := range se.order {
		if st.bound || !reachable[st.key()] {
			continue
		}
		count := 0
		for _, v := range st.versions {
			if st.satisfiesAll(v) {
				count++
			}
		}
		if best == nil || count < bestCount ||
			(count == bestCount && laneLess(st, best)) {
			best, bestCount = st, count
		}
	}
	return best
}

// reachableFrom computes lanes reachable via selected-release dependency
// edges starting exactly from the lanes on the path.
func (se *search) reachableFrom(path []*laneState) map[string]bool {
	seen := map[string]bool{}
	var queue []*laneState
	inQueue := map[string]bool{}
	for _, st := range path {
		queue = append(queue, st)
		inQueue[st.key()] = true
	}
	for len(queue) > 0 {
		st := queue[0]
		queue = queue[1:]
		if seen[st.key()] {
			continue
		}
		seen[st.key()] = true
		if !st.bound {
			continue
		}
		rel, err := se.release(st.lane.Package, st.chosen)
		if err != nil {
			continue
		}
		for _, dep := range rel.Deps {
			for _, t := range se.pkgLs[dep.Name] {
				if !inQueue[t.key()] && !seen[t.key()] {
					queue = append(queue, t)
					inQueue[t.key()] = true
				}
			}
		}
	}
	return seen
}

// feasible returns candidate versions that satisfy the declarations visible
// given the ancestor path (seed decls at depth -1 plus propagated decls).
func (se *search) feasible(st *laneState, ancestors []*laneState) []semver.Version {
	out := make([]semver.Version, 0, len(st.versions))
	for i := len(st.versions) - 1; i >= 0; i-- {
		v := st.versions[i]
		if st.satisfiesAll(v) {
			out = append(out, v)
		}
	}
	return out
}

// stateSnap is a restorable search snapshot.
type stateSnap struct {
	order []*laneState
	lanes map[string]*laneState
	pkgLs map[string]map[string]*laneState
	decls map[string][]graph.Declaration
	bound map[string]semver.Version
}

func (se *search) snapshot() *stateSnap {
	snap := &stateSnap{
		order: append([]*laneState(nil), se.order...),
		lanes: map[string]*laneState{},
		pkgLs: map[string]map[string]*laneState{},
		decls: map[string][]graph.Declaration{},
		bound: map[string]semver.Version{},
	}
	for k, st := range se.lanes {
		snap.lanes[k] = st
		cp := make([]graph.Declaration, len(st.decls))
		copy(cp, st.decls)
		snap.decls[k] = cp
		if st.bound {
			snap.bound[k] = st.chosen
		}
	}
	for p, m := range se.pkgLs {
		cp := map[string]*laneState{}
		for k, v := range m {
			cp[k] = v
		}
		snap.pkgLs[p] = cp
	}
	return snap
}

// restore returns the search to snap, then unbinds the retried lane.
func (se *search) restore(snap *stateSnap, retry *laneState) {
	se.order = append([]*laneState(nil), snap.order...)
	se.lanes = snap.lanes
	se.pkgLs = snap.pkgLs
	for k, st := range se.lanes {
		if cp, ok := snap.decls[k]; ok {
			st.decls = append([]graph.Declaration(nil), cp...)
		} else {
			st.decls = nil
		}
		if v, was := snap.bound[k]; was {
			st.bound, st.chosen, st.color = true, v, laneBlack
		} else if st != retry {
			st.bound = false
			st.color = laneWhite
			var zero semver.Version
			st.chosen = zero
			st.applied = map[string]bool{}
		}
	}
	// The retried lane itself must be unbound for the next candidate.
	retry.bound = false
	retry.color = laneWhite
	var zero semver.Version
	retry.chosen = zero
	retry.applied = map[string]bool{}
	retry.decision = Decision{}
	se.rebuildReach()
}

// rebuildReach recomputes the reachable set from the currently bound lanes
// after a backtracking restore.
func (se *search) rebuildReach() {
	se.reach = map[string]bool{}
	for _, st := range se.order {
		if st.bound {
			se.reach[st.key()] = true
			se.extendReach(st)
		}
	}
}

// extendReach adds every lane of every dependency of a bound lane to the
// incrementally maintained reachable set.
func (se *search) extendReach(st *laneState) {
	rel, err := se.release(st.lane.Package, st.chosen)
	if err != nil {
		return
	}
	for _, dep := range rel.Deps {
		for _, t := range se.pkgLs[dep.Name] {
			se.reach[t.key()] = true
		}
	}
}

// propagate routes the selected release's dependency declarations and
// detects cycles against the recursion path.
func (se *search) propagate(st *laneState, path []*laneState) error {
	rel, err := se.release(st.lane.Package, st.chosen)
	if err != nil {
		return err
	}
	for _, dep := range rel.Deps {
		edgeID := rel.Package + "@" + rel.Version.String() + "->" + dep.Name
		if st.applied[edgeID] {
			continue
		}
		st.applied[edgeID] = true
		if _, err := se.versions(dep.Name); err != nil {
			if _, ok := err.(*registry.ErrPackageMissing); ok {
				rng, _ := semver.ParseRange(dep.Range)
				src := manifest.Source{Kind: "package",
					Package_: rel.Package + "@" + rel.Version.String(), Range: dep.Range}
				return missingPackageFailure(dep.Name,
					[]graph.Declaration{{Range: rng, RawRange: dep.Range, Src: src}})
			}
			return err
		}
		rng, err := semver.ParseRange(dep.Range)
		if err != nil {
			return &Failure{Kind: KindInvalidRange, Package: dep.Name, Version: dep.Range,
				Message: fmt.Sprintf("release %s@%s declares invalid range %q for %s",
					rel.Package, rel.Version, dep.Range, dep.Name)}
		}
		before := se.existingKeys(dep.Name)
		decl := graph.Declaration{Range: rng, RawRange: dep.Range, Src: manifest.Source{
			Kind: "package", Package_: rel.Package + "@" + rel.Version.String(), Range: dep.Range}}
		if err := se.routeDecls(dep.Name, []graph.Declaration{decl}); err != nil {
			return err
		}
		for lk, t := range se.pkgLs[dep.Name] {
			if !before[lk] && !laneHasAny(t) {
				return intersectionFailure(t)
			}
			if t.bound && !satisfiedByAll(t, t.chosen) {
				return intersectionFailure(t)
			}
		}
	}
	return nil
}

func (se *search) existingKeys(pkg string) map[string]bool {
	out := map[string]bool{}
	for k := range se.pkgLs[pkg] {
		out[k] = true
	}
	return out
}

func laneLess(a, b *laneState) bool {
	if a.lane.Package != b.lane.Package {
		return a.lane.Package < b.lane.Package
	}
	return a.lane.Key() < b.lane.Key()
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

func (se *search) buildResult() *Result {
	res := &Result{}
	keys := make([]string, 0, len(se.lanes))
	for k := range se.lanes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		st := se.lanes[k]
		if !st.bound {
			continue
		}
		res.Selections = append(res.Selections, Selection{
			Package: st.lane.Package,
			Version: st.chosen,
			Lane:    st.lane,
			Reasons: sourcesOf(st.allDecls()),
		})
		if st.decision.Package != "" {
			d := st.decision
			d.Chosen = st.chosen
			d.Constraint = intersectionString(st.allDecls())
			d.Reasons = sourcesOf(st.allDecls())
			res.Decisions = append(res.Decisions, d)
		}
	}
	res.InputsHash = hashInputs(se.g, se.versionsCache, se.s.opt)
	return res
}

func (st *laneState) allDecls() []graph.Declaration {
	return append([]graph.Declaration(nil), st.decls...)
}

func versionsDesc(vs semver.VersionList) []string {
	out := make([]string, 0, len(vs))
	for i := len(vs) - 1; i >= 0; i-- {
		out = append(out, vs[i].String())
	}
	return out
}

func intersectionString(decls []graph.Declaration) string {
	parts := make([]string, 0, len(decls))
	for _, d := range decls {
		parts = append(parts, d.RawRange)
	}
	sort.Strings(parts)
	return strings.Join(parts, " ")
}

func sourcesOf(decls []graph.Declaration) []manifest.Source {
	type sk struct{ kind, pkg, member, file, rng string }
	seen := map[sk]bool{}
	var out []manifest.Source
	for _, d := range decls {
		k := sk{d.Src.Kind, d.Src.Package_, d.Src.Member, d.Src.File, d.RawRange}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, d.Src)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Package_ != b.Package_ {
			return a.Package_ < b.Package_
		}
		return a.Range < b.Range
	})
	return out
}

// SolveRoots builds the graph from root manifests and a manifest reader,
// then solves it.
func (s *Solver) SolveRoots(ctx context.Context, roots []manifest.Manifest, rd manifest.Reader) (*Result, error) {
	g, err := graph.BuildFromManifests(ctx, roots, rd)
	if err != nil {
		if ie, ok := err.(*graph.InvalidRangeError); ok {
			return nil, &Failure{Kind: KindInvalidRange, Message: ie.Error(),
				Package: ie.Package, Version: ie.Range, wrapped: ie}
		}
		return nil, err
	}
	return s.Solve(ctx, g)
}
