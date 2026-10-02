// Command depresolve is a small local-only CLI around the resolver:
//
//	depresolve resolve --manifest m.json --index index.json [--out dep.lock]
//	depresolve verify  --manifest m.json --index index.json --lock dep.lock
//	depresolve install --index index.json --lock dep.lock --root node_modules
//	depresolve workspace --workspace ws.json --index index.json [--out dep.lock]
//
// All version information is read from local files; no network is used.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/Diudiuseven/dependency-resolution-go/lock"
	"github.com/Diudiuseven/dependency-resolution-go/manifest"
	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/resolve"
	"github.com/Diudiuseven/dependency-resolution-go/solver"
	"github.com/Diudiuseven/dependency-resolution-go/workspace"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	var (
		manifestPath = fs.String("manifest", "", "path to root manifest JSON")
		indexPath    = fs.String("index", "", "path to local registry index JSON")
		lockPath     = fs.String("lock", "dep.lock", "path to lock file")
		outPath      = fs.String("out", "", "lock output path (defaults to --lock)")
		installRoot  = fs.String("root", "node_modules", "install root directory")
		wsPath       = fs.String("workspace", "", "path to workspace JSON")
	)
	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	ctx := context.Background()
	if err := run(ctx, cmd, params{
		manifest: *manifestPath, index: *indexPath, lock: *lockPath,
		out: *outPath, root: *installRoot, workspace: *wsPath,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		if f, ok := solver.AsFailure(err); ok {
			fmt.Fprintln(os.Stderr, "kind:", f.Kind)
			for _, c := range f.Contradictions {
				fmt.Fprintln(os.Stderr, "  contradiction:", c.String())
			}
			if len(f.Cycle) > 0 {
				fmt.Fprintln(os.Stderr, "  cycle:", join(f.Cycle))
			}
		}
		os.Exit(1)
	}
}

type params struct {
	manifest, index, lock, out, root, workspace string
}

func run(ctx context.Context, cmd string, p params) error {
	switch cmd {
	case "resolve":
		return cmdResolve(ctx, p)
	case "verify":
		return cmdVerify(ctx, p)
	case "install":
		return cmdInstall(ctx, p)
	case "workspace":
		return cmdWorkspace(ctx, p)
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func loadIndex(path string) (*registry.MemoryRegistry, error) {
	if path == "" {
		return registry.NewMemory(nil)
	}
	return registry.LoadIndexFile(path)
}

func cmdResolve(ctx context.Context, p params) error {
	if p.manifest == "" {
		return errors.New("--manifest is required")
	}
	m, err := manifest.LoadFile(p.manifest)
	if err != nil {
		return err
	}
	reg, err := loadIndex(p.index)
	if err != nil {
		return err
	}
	out := p.out
	if out == "" {
		out = p.lock
	}
	r := resolve.New(resolve.Config{
		Registry: reg, Reader: manifest.NewStaticReader(nil),
		LockPath: out, VerifyPolicy: lock.PolicyFail})
	f, res, err := r.FullRun(ctx, []manifest.Manifest{m})
	if err != nil {
		return err
	}
	fmt.Printf("resolved %d units; inputs=%s lock=%s\n",
		len(res.Selections), res.InputsHash, f.ID())
	for _, s := range res.Selections {
		fmt.Printf("  %s@%s (%s)\n", s.Package, s.Version, s.Lane.Key())
	}
	return nil
}

func cmdVerify(ctx context.Context, p params) error {
	if p.manifest == "" {
		return errors.New("--manifest is required")
	}
	m, err := manifest.LoadFile(p.manifest)
	if err != nil {
		return err
	}
	reg, err := loadIndex(p.index)
	if err != nil {
		return err
	}
	r := resolve.New(resolve.Config{Registry: reg,
		Reader: manifest.NewStaticReader(nil)})
	_, res, err := r.FullRun(ctx, []manifest.Manifest{m})
	if err != nil {
		return err
	}
	f, err := lock.LoadFile(p.lock)
	if err != nil {
		return fmt.Errorf("load lock: %w", err)
	}
	rep, err := lock.Verify(ctx, res, f)
	if err != nil {
		return err
	}
	if rep.OK {
		fmt.Println("lock is up to date")
		return nil
	}
	for _, d := range rep.Drifts {
		fmt.Println("drift:", d.String())
	}
	return errors.New("lock is out of sync (run with --refresh to update)")
}

func cmdInstall(ctx context.Context, p params) error {
	reg, err := loadIndex(p.index)
	if err != nil {
		return err
	}
	f, err := lock.LoadFile(p.lock)
	if err != nil {
		return err
	}
	plan, err := lock.PlanInstall(ctx, f, reg)
	if err != nil {
		return err
	}
	in := lock.NewInstaller(reg, p.root)
	res, err := in.Install(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Printf("installed %d, skipped %d\n", len(res.Installed), len(res.Skipped))
	rep, err := in.VerifyInstalled(ctx, f)
	if err != nil {
		return err
	}
	if !rep.OK {
		return fmt.Errorf("post-install verification found %d problems", len(rep.Drifts))
	}
	fmt.Println("installation verified")
	return nil
}

// wsFile is the workspace JSON layout.
type wsFile struct {
	Members []wsMember `json:"members"`
}
type wsMember struct {
	Path     string        `json:"path"`
	Package  string        `json:"package"`
	Manifest manifest.File `json:"manifest"`
}

func cmdWorkspace(ctx context.Context, p params) error {
	if p.workspace == "" {
		return errors.New("--workspace is required")
	}
	data, err := os.ReadFile(p.workspace)
	if err != nil {
		return err
	}
	var wsf wsFile
	if err := json.Unmarshal(data, &wsf); err != nil {
		return err
	}
	members := make([]workspace.Member, 0, len(wsf.Members))
	for _, mm := range wsf.Members {
		m, err := manifest.FromFile(mustMarshal(mm.Manifest), p.workspace+":"+mm.Path)
		if err != nil {
			return err
		}
		m.Package = mm.Package
		members = append(members, workspace.Member{
			Path: mm.Path, PackageName: mm.Package, Manifest: m})
	}
	ws, err := workspace.New(members)
	if err != nil {
		return err
	}
	reg, err := loadIndex(p.index)
	if err != nil {
		return err
	}
	out := p.out
	if out == "" {
		out = p.lock
	}
	r := resolve.New(resolve.Config{Registry: reg,
		Reader: manifest.NewStaticReader(nil), LockPath: out})
	f, res, err := r.FullRunWorkspace(ctx, ws)
	if err != nil {
		return err
	}
	fmt.Printf("workspace resolved %d units; lock=%s\n", len(res.Selections), f.ID())
	return nil
}

func mustMarshal(f manifest.File) []byte {
	b, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	return b
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " -> "
		}
		out += x
	}
	return out
}

func usage() {
	fmt.Fprint(os.Stderr, `depresolve - deterministic local dependency resolution

usage:
  depresolve resolve   --manifest m.json --index index.json [--out dep.lock]
  depresolve verify    --manifest m.json --index index.json --lock dep.lock
  depresolve install   --index index.json --lock dep.lock [--root node_modules]
  depresolve workspace --workspace ws.json --index index.json [--out dep.lock]
`)
}
