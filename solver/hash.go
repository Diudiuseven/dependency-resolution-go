package solver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/Diudiuseven/dependency-resolution-go/compat"
	"github.com/Diudiuseven/dependency-resolution-go/graph"
	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

// hashInputs produces a stable hash of everything that can change an
// answer: graph declarations (package edges, raw ranges, sources), the
// locally available version catalogue, and the compatibility config. Map
// iteration never enters the hash: every collection is sorted first.
func hashInputs(g *graph.Graph, versions map[string]semver.VersionList, opt Options) string {
	h := sha256.New()
	writeLine := func(s string) { h.Write([]byte(s)); h.Write([]byte{'\n'}) }

	writeLine("config:")
	writeLine("  compat=" + compatName(opt.Compat))
	for _, p := range sortedConfigPackages(opt.Compat) {
		writeLine("  override:" + p + "=" + fmt.Sprintf("%T", opt.Compat.PerPackage[p]))
	}
	pkgs := make([]string, 0, len(versions))
	for p := range versions {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	writeLine("catalog:")
	for _, p := range pkgs {
		vs := versions[p].Sorted()
		strs := make([]string, len(vs))
		for i, v := range vs {
			strs[i] = v.String()
		}
		writeLine("  " + p + "=" + strings.Join(strs, ","))
	}
	writeLine("graph-roots:" + strings.Join(g.Roots, ","))
	writeLine("graph-edges:")
	for _, from := range g.Nodes() {
		n, _ := g.Node(from)
		for _, e := range n.Edges {
			ranges := make([]string, 0, len(e.Decls))
			for _, d := range e.Decls {
				ranges = append(ranges, d.RawRange+"@"+sourceName(d.Src))
			}
			sort.Strings(ranges)
			writeLine("  " + from + "->" + e.To + " " + strings.Join(ranges, ";"))
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func compatName(c compat.Config) string {
	if c.Rule == nil {
		return "strict-major"
	}
	return fmt.Sprintf("%T", c.Rule)
}

func sortedConfigPackages(c compat.Config) []string {
	out := make([]string, 0, len(c.PerPackage))
	for p := range c.PerPackage {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func sourceName(s manifest.Source) string {
	return strings.Join([]string{s.Kind, s.Member, s.Package_, s.File, s.Range}, "|")
}
