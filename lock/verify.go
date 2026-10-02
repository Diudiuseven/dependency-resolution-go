package lock

import (
	"context"
	"fmt"
	"sort"

	"github.com/Diudiuseven/dependency-resolution-go/solver"
)

// DriftKind classifies a difference between manifest world and lock.
type DriftKind string

const (
	DriftMissing DriftKind = "missing" // selected by solve but absent from lock
	DriftExtra   DriftKind = "extra"   // present in lock but no longer selected
	DriftChanged DriftKind = "changed" // same package/lane, different version/hash
)

// Drift is one detected lock/manifest discrepancy.
type Drift struct {
	Kind    DriftKind
	Package string
	Lane    string
	Have    string // what the lock records
	Want    string // what the solve requires
	Reason  string
}

func (d Drift) String() string {
	switch d.Kind {
	case DriftMissing:
		return fmt.Sprintf("missing from lock: %s#%s (wanted %s)", d.Package, d.Lane, d.Want)
	case DriftExtra:
		return fmt.Sprintf("extra in lock: %s#%s (locked %s, no longer required)", d.Package, d.Lane, d.Have)
	default:
		return fmt.Sprintf("drift on %s#%s: lock has %s but resolution wants %s",
			d.Package, d.Lane, d.Have, d.Want)
	}
}

// Policy defines what verification does with drift.
type Policy int

const (
	// PolicyFail rejects any drift.
	PolicyFail Policy = iota
	// PolicyRefresh re-resolves and rewrites the lock on drift.
	PolicyRefresh
	// PolicyRefetch keeps the locked versions but treats missing artifacts
	// as install-time work (version drift still fails).
	PolicyRefetch
)

// Report is the outcome of Verify.
type Report struct {
	OK       bool
	Drifts   []Drift
	LockHash string
}

// unitIndex indexes lock units by package#lane.
type unitEntry struct {
	unit Unit
	ok   bool
}

func indexFile(f *File) map[string]unitEntry {
	m := map[string]unitEntry{}
	if f == nil {
		return m
	}
	for _, u := range f.Units {
		m[u.key()] = unitEntry{unit: u, ok: true}
	}
	return m
}

// Verify compares a fresh solve result against a lock file.
func Verify(ctx context.Context, res *solver.Result, f *File) (*Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rep := &Report{}
	locked := indexFile(f)
	if f != nil {
		rep.LockHash = f.ID()
	}
	seen := map[string]bool{}
	for _, sel := range res.Selections {
		k := sel.Package + "#" + sel.Lane.Key()
		seen[k] = true
		have, ok := locked[k]
		if !ok {
			rep.Drifts = append(rep.Drifts, Drift{
				Kind: DriftMissing, Package: sel.Package,
				Lane: sel.Lane.Key(), Want: sel.Version.String()})
			continue
		}
		if have.unit.Version != sel.Version.String() {
			rep.Drifts = append(rep.Drifts, Drift{
				Kind: DriftChanged, Package: sel.Package,
				Lane: sel.Lane.Key(), Have: have.unit.Version,
				Want:   sel.Version.String(),
				Reason: "resolved version differs from lock",
			})
		}
	}
	for k, e := range locked {
		if seen[k] {
			continue
		}
		rep.Drifts = append(rep.Drifts, Drift{
			Kind: DriftExtra, Package: e.unit.Package,
			Lane: laneOfKey(e.unit), Have: e.unit.Version})
	}
	sortDrifts(rep.Drifts)
	rep.OK = len(rep.Drifts) == 0
	return rep, nil
}

func laneOfKey(u Unit) string {
	if u.Major == 0 && u.Minor != 0 {
		return fmt.Sprintf("0.%d", u.Minor)
	}
	return fmt.Sprintf("%d", u.Major)
}

func sortDrifts(ds []Drift) {
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Package != ds[j].Package {
			return ds[i].Package < ds[j].Package
		}
		if ds[i].Lane != ds[j].Lane {
			return ds[i].Lane < ds[j].Lane
		}
		return ds[i].Kind < ds[j].Kind
	})
}

// ApplyPolicy produces a lock consistent with res according to policy.
// Under PolicyFail any drift is returned as an error (the caller keeps the
// old lock untouched). PolicyRefresh regenerates the lock. PolicyRefetch
// regenerates only when versions drifted; purely missing artifacts are
// tolerated (reported) for the install step.
func ApplyPolicy(ctx context.Context, res *solver.Result, f *File, policy Policy) (*File, []Drift, error) {
	rep, err := Verify(ctx, res, f)
	if err != nil {
		return nil, nil, err
	}
	if rep.OK {
		return f, nil, nil
	}
	switch policy {
	case PolicyFail:
		return nil, rep.Drifts, &PolicyError{Drifts: rep.Drifts}
	case PolicyRefresh:
		return FromResult(res), rep.Drifts, nil
	case PolicyRefetch:
		for _, d := range rep.Drifts {
			if d.Kind == DriftChanged {
				return nil, rep.Drifts, &PolicyError{Drifts: rep.Drifts}
			}
		}
		// Missing/extra units still require rewriting the lock to match.
		return FromResult(res), rep.Drifts, nil
	}
	return nil, rep.Drifts, fmt.Errorf("unknown lock policy %d", policy)
}

// PolicyError reports that drift violated the configured policy.
type PolicyError struct{ Drifts []Drift }

func (e *PolicyError) Error() string {
	msg := "lock is out of sync with manifests:"
	for _, d := range e.Drifts {
		msg += "\n  - " + d.String()
	}
	return msg
}
