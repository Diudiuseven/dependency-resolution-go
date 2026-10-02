package solver

import (
	"context"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
)

// Cycles formed purely through registry release dependencies must be caught
// too, not just manifest-level edges.
func TestTransitiveCycleDetected(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{
		rel("a", "1.0.0", registry.Dep{Name: "b", Range: "^1.0.0"}),
		rel("b", "1.0.0", registry.Dep{Name: "c", Range: "^1.0.0"}),
		rel("c", "1.0.0", registry.Dep{Name: "a", Range: "^1.0.0"}),
	})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app", dep("a", "^1.0.0"))})
	_, err := s.Solve(context.Background(), g)
	f, ok := AsFailure(err)
	if !ok || f.Kind != KindCycle {
		t.Fatalf("want cycle through transitive deps, got %v", err)
	}
}

func TestSelfDependencyCycleDetected(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{
		rel("a", "1.0.0", registry.Dep{Name: "a", Range: "^1.0.0"}),
	})
	s := New(reg, DefaultOptions())
	g := buildGraph(t, []manifest.Manifest{rootManifest("app", dep("a", "^1.0.0"))})
	_, err := s.Solve(context.Background(), g)
	f, ok := AsFailure(err)
	if !ok || f.Kind != KindCycle {
		t.Fatalf("want self-cycle, got %v", err)
	}
}
