package lock

import "github.com/Diudiuseven/dependency-resolution-go/graph"
import "github.com/Diudiuseven/dependency-resolution-go/manifest"

type graphBuilder struct{}

func (g *graphBuilder) with(ms ...manifest.Manifest) (*graph.Graph, error) {
	b := graph.NewBuilder()
	for i, m := range ms {
		if i == 0 {
			b.AddRoot(m.Package)
		}
		if err := b.AddManifest(m); err != nil {
			return nil, err
		}
	}
	return b.Build(), nil
}
