package solver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
)

// TestLargeDiamondGraph builds a wide, deep diamond DAG and asserts the
// solver completes in bounded time with the expected number of units.
func TestLargeDiamondGraph(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	var rels []registry.Release
	const width = 40
	const depth = 12
	// Layers: L{d}-{i}, each package has 3 versions; edges connect every
	// node to all nodes in the next layer with permissive ranges, producing
	// a dense but always-satisfiable DAG.
	for d := 0; d < depth; d++ {
		for i := 0; i < width; i++ {
			name := fmt.Sprintf("L%d-%d", d, i)
			for _, v := range []string{"1.0.0", "1.5.0", "1.9.0"} {
				var deps []registry.Dep
				if d+1 < depth {
					for j := 0; j < width; j++ {
						deps = append(deps, registry.Dep{Name: fmt.Sprintf("L%d-%d", d+1, j), Range: "^1.0.0"})
					}
				}
				rels = append(rels, rel(name, v, deps...))
			}
		}
	}
	reg := mustRegistry(t, rels)
	opt := DefaultOptions()
	opt.MaxBacktracks = 200000
	opt.MaxSteps = 5_000_000
	s := New(reg, opt)
	m := rootManifest("app")
	for j := 0; j < width; j++ {
		m.Deps = append(m.Deps, dep(fmt.Sprintf("L0-%d", j), "^1.0.0"))
	}
	g := buildGraph(t, []manifest.Manifest{m})
	start := time.Now()
	res, err := s.Solve(context.Background(), g)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if want := width * depth; len(res.Selections) != want {
		t.Fatalf("selected %d units want %d", len(res.Selections), want)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("solve took too long: %s", elapsed)
	}
	t.Logf("solved %d units in %s", len(res.Selections), elapsed)
}
