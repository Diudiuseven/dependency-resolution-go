package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Diudiuseven/dependency-resolution-go/resolver"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "resolve":
		cmdResolve(os.Args[2:])
	case "install":
		cmdInstall(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("depres - 确定性依赖版本解析工具")
	fmt.Println()
	fmt.Println("用法:")
	fmt.Println("  depres resolve [flags]    求解依赖图并生成锁定文件")
	fmt.Println("  depres install [flags]    根据锁定文件执行安装")
	fmt.Println("  depres verify [flags]     校验清单与锁定文件的一致性")
	fmt.Println()
	fmt.Println("resolve  flags:")
	fmt.Println("  -registry <dir>     本地 registry 目录 (必需)")
	fmt.Println("  -manifest <file>    模块清单文件 (可多次指定)")
	fmt.Println("  -workspace <file>   工作区配置文件")
	fmt.Println("  -policy <name>      兼容策略: major|minor|exact|unified (默认 major)")
	fmt.Println("  -lock <file>        输出锁定文件路径 (默认 depres-lock.json)")
	fmt.Println("  -explain            输出可读说明")
	fmt.Println()
	fmt.Println("install flags:")
	fmt.Println("  -registry <dir>     本地 registry 目录 (必需)")
	fmt.Println("  -lock <file>        锁定文件路径 (默认 depres-lock.json)")
	fmt.Println("  -target <dir>       安装目标目录 (默认 node_modules)")
	fmt.Println()
	fmt.Println("verify flags:")
	fmt.Println("  -registry <dir>     本地 registry 目录 (必需)")
	fmt.Println("  -manifest <file>    模块清单文件 (可多次指定)")
	fmt.Println("  -workspace <file>   工作区配置文件")
	fmt.Println("  -lock <file>        锁定文件路径 (默认 depres-lock.json)")
	fmt.Println("  -policy <name>      处理策略: strict|warn|update (默认 strict)")
}

func cmdResolve(args []string) {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	registryDir := fs.String("registry", "", "本地 registry 目录")
	lockPath := fs.String("lock", "depres-lock.json", "输出锁定文件路径")
	policyName := fs.String("policy", "major", "兼容策略")
	explain := fs.Bool("explain", false, "输出可读说明")
	var manifests stringList
	var workspace string
	fs.Var(&manifests, "manifest", "模块清单文件")
	fs.StringVar(&workspace, "workspace", "", "工作区配置文件")
	fs.Parse(args)

	if *registryDir == "" {
		fmt.Fprintln(os.Stderr, "错误: 必须指定 -registry")
		os.Exit(1)
	}

	policy, ok := resolver.PolicyByName(*policyName)
	if !ok {
		fmt.Fprintf(os.Stderr, "错误: 未知策略 %q\n", *policyName)
		os.Exit(1)
	}

	registry := resolver.NewFileRegistry(*registryDir)

	var roots []resolver.DependencyEdge
	var memberNames []string

	if workspace != "" {
		ws, err := resolver.LoadWorkspace(workspace)
		if err != nil {
			fatal(err)
		}
		members, err := ws.LoadMembers()
		if err != nil {
			fatal(err)
		}
		res, err := resolver.ResolveMembers(members, registry, policy)
		if err != nil {
			handleError(err)
		}
		for _, m := range members {
			memberNames = append(memberNames, m.Manifest.Name)
		}
		lock, err := resolver.LockFromResolution(res, registry, policy, memberNames)
		if err != nil {
			fatal(err)
		}
		if err := resolver.WriteLock(*lockPath, lock); err != nil {
			fatal(err)
		}
		if *explain {
			fmt.Print(resolver.ExplainResolution(res))
		}
		fmt.Printf("锁定文件已写入: %s\n", *lockPath)
		return
	}

	for _, mf := range manifests {
		data, err := os.ReadFile(mf)
		if err != nil {
			fatal(err)
		}
		m, err := resolver.ParseModuleManifest(data)
		if err != nil {
			fatal(err)
		}
		source := resolver.Source{Kind: "root", Name: m.Name, Path: mf}
		roots = append(roots, m.Edges(source)...)
		memberNames = append(memberNames, m.Name)
	}

	solver := resolver.NewSolver(registry, policy)
	res, err := solver.Solve(roots)
	if err != nil {
		handleError(err)
	}

	lock, err := resolver.LockFromResolution(res, registry, policy, memberNames)
	if err != nil {
		fatal(err)
	}
	if err := resolver.WriteLock(*lockPath, lock); err != nil {
		fatal(err)
	}
	if *explain {
		fmt.Print(resolver.ExplainResolution(res))
	}
	fmt.Printf("锁定文件已写入: %s\n", *lockPath)
}

func cmdInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	registryDir := fs.String("registry", "", "本地 registry 目录")
	lockPath := fs.String("lock", "depres-lock.json", "锁定文件路径")
	targetDir := fs.String("target", "node_modules", "安装目标目录")
	fs.Parse(args)

	if *registryDir == "" {
		fmt.Fprintln(os.Stderr, "错误: 必须指定 -registry")
		os.Exit(1)
	}

	registry := resolver.NewFileRegistry(*registryDir)
	lock, err := readLockWithWarnings(*lockPath)
	if err != nil {
		fatal(err)
	}

	plan, err := resolver.Install(lock, registry, *targetDir)
	if err != nil {
		fatal(err)
	}

	// 物化安装：创建目录并写入包清单
	for _, slot := range plan.Slots {
		dir := slot.Path
		if err := os.MkdirAll(dir, 0755); err != nil {
			fatal(err)
		}
		manifest, err := registry.Manifest(slot.Name, slot.Version)
		if err != nil {
			fatal(err)
		}
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), data, 0644); err != nil {
			fatal(err)
		}
	}

	fmt.Printf("安装完成: %d 个包 -> %s\n", len(plan.Slots), *targetDir)
}

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	registryDir := fs.String("registry", "", "本地 registry 目录")
	lockPath := fs.String("lock", "depres-lock.json", "锁定文件路径")
	policyName := fs.String("policy", "strict", "处理策略")
	var manifests stringList
	var workspace string
	fs.Var(&manifests, "manifest", "模块清单文件")
	fs.StringVar(&workspace, "workspace", "", "工作区配置文件")
	fs.Parse(args)

	if *registryDir == "" {
		fmt.Fprintln(os.Stderr, "错误: 必须指定 -registry")
		os.Exit(1)
	}

	registry := resolver.NewFileRegistry(*registryDir)
	lock, err := readLockWithWarnings(*lockPath)
	if err != nil {
		fatal(err)
	}

	var moduleManifests []*resolver.ModuleManifest
	if workspace != "" {
		ws, err := resolver.LoadWorkspace(workspace)
		if err != nil {
			fatal(err)
		}
		members, err := ws.LoadMembers()
		if err != nil {
			fatal(err)
		}
		for _, m := range members {
			moduleManifests = append(moduleManifests, m.Manifest)
		}
	} else {
		for _, mf := range manifests {
			data, err := os.ReadFile(mf)
			if err != nil {
				fatal(err)
			}
			m, err := resolver.ParseModuleManifest(data)
			if err != nil {
				fatal(err)
			}
			moduleManifests = append(moduleManifests, m)
		}
	}

	result, err := resolver.Verify(moduleManifests, lock, registry)
	if err != nil {
		fatal(err)
	}

	fmt.Print(result.Summary())
	if result.HasIssues() {
		switch *policyName {
		case "strict":
			os.Exit(1)
		case "warn":
			os.Exit(0)
		case "update":
			// 重新求解
			policy, ok := resolver.PolicyByName(lock.Policy)
			if !ok {
				policy = resolver.PolicyMajor
			}
			var roots []resolver.DependencyEdge
			for _, m := range moduleManifests {
				source := resolver.Source{Kind: "workspace", Name: m.Name}
				roots = append(roots, m.Edges(source)...)
			}
			solver := resolver.NewSolver(registry, policy)
			res, err := solver.Solve(roots)
			if err != nil {
				handleError(err)
			}
			newLock, err := resolver.LockFromResolution(res, registry, policy, lock.Members)
			if err != nil {
				fatal(err)
			}
			if err := resolver.WriteLock(*lockPath, newLock); err != nil {
				fatal(err)
			}
			fmt.Println("锁定文件已更新")
		default:
			fmt.Fprintf(os.Stderr, "错误: 未知策略 %q\n", *policyName)
			os.Exit(1)
		}
	}
}

// stringList 用于多次指定的 flag。
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// readLockWithWarnings 读取锁定文件，并将旧格式兼容提示输出到 stderr。
func readLockWithWarnings(path string) (*resolver.Lock, error) {
	lock, warnings, err := resolver.ReadLockCompat(path)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "兼容提示: %s\n", w)
	}
	return lock, nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "错误: %v\n", err)
	os.Exit(1)
}

func handleError(err error) {
	switch e := err.(type) {
	case *resolver.ConflictError:
		fmt.Fprint(os.Stderr, resolver.ExplainConflict(e))
	case *resolver.CycleError:
		fmt.Fprint(os.Stderr, resolver.ExplainCycle(e))
	case *resolver.MissingDepError:
		fmt.Fprint(os.Stderr, resolver.ExplainMissingDep(e))
	case *resolver.InvalidRangeError:
		fmt.Fprint(os.Stderr, resolver.ExplainInvalidRange(e))
	default:
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
	}
	os.Exit(1)
}
