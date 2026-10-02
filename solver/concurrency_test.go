package solver

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
)

func encodeResult(res *Result) (string, error) {
	parts := make([]string, 0, len(res.Selections))
	for _, k := range selectionsSortedKeys(res) {
		if sel, ok := res.SelectionFor(strings.Split(k, "#")[0], laneKeyOf(k)); ok {
			parts = append(parts, k+"="+sel.Version.String())
		}
	}
	sort.Strings(parts)
	return res.InputsHash + "|" + strings.Join(parts, ","), nil
}

func selectionsSortedKeys(res *Result) []string {
	out := make([]string, 0, len(res.Selections))
	for _, sel := range res.Selections {
		out = append(out, sel.Package+"#"+sel.Lane.Key())
	}
	sort.Strings(out)
	return out
}

func laneKeyOf(k string) string {
	i := strings.Index(k, "#")
	return k[i+1:]
}

// TestConcurrentDeterminism runs many solves concurrently with deliberately
// shuffled dependency declaration order and asserts every result is equal.
func TestConcurrentDeterminism(t *testing.T) {
	reg := mustRegistry(t, []registry.Release{
		rel("lib", "1.0.0"), rel("lib", "1.9.0"), rel("lib", "2.0.0"),
		rel("a", "1.0.0", registry.Dep{Name: "lib", Range: "^1.0.0"}),
		rel("b", "1.0.0", registry.Dep{Name: "lib", Range: "1.x"}),
		rel("c", "1.0.0", registry.Dep{Name: "a", Range: "^1.0.0"},
			registry.Dep{Name: "b", Range: "^1.0.0"}),
	})
	results := make(chan string, 40)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := New(reg, DefaultOptions())
			d1, d2 := dep("a", "^1.0.0"), dep("c", "^1.0.0")
			if i%2 == 0 {
				d1, d2 = d2, d1
			}
			g := buildGraph(t, []manifest.Manifest{rootManifest("app", d1, d2)})
			res, err := s.Solve(context.Background(), g)
			if err != nil {
				t.Error(err)
				return
			}
			data, err := encodeResult(res)
			if err != nil {
				t.Error(err)
				return
			}
			results <- data
		}(i)
	}
	wg.Wait()
	close(results)
	var first string
	for r := range results {
		if first == "" {
			first = r
		} else if r != first {
			t.Fatalf("nondeterministic concurrent results:\n%s\n%s", first, r)
		}
	}
}
