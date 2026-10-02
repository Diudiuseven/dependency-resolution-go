package resolver

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Workspace 表示一个工作区，包含多个成员。
type Workspace struct {
	Members []string // 成员清单文件路径
	root    string
}

// WorkspaceMember 表示工作区成员。
type WorkspaceMember struct {
	Manifest *ModuleManifest
	Path     string
}

// workspaceFile 工作区配置文件格式。
type workspaceFile struct {
	Members []string `json:"members"`
}

// LoadWorkspace 加载工作区及其成员清单。
func LoadWorkspace(path string) (*Workspace, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取工作区配置失败: %v", err)
	}
	var wf workspaceFile
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("解析工作区配置失败: %v", err)
	}
	root := filepath.Dir(path)
	ws := &Workspace{
		Members: wf.Members,
		root:    root,
	}
	return ws, nil
}

// LoadMembers 加载所有成员清单。
func (ws *Workspace) LoadMembers() ([]*WorkspaceMember, error) {
	var members []*WorkspaceMember
	for _, memberPath := range ws.Members {
		fullPath := memberPath
		if !filepath.IsAbs(fullPath) {
			fullPath = filepath.Join(ws.root, memberPath)
		}
		data, err := os.ReadFile(fullPath)
		if err != nil {
			return nil, fmt.Errorf("读取成员清单失败 %s: %v", fullPath, err)
		}
		m, err := ParseModuleManifest(data)
		if err != nil {
			return nil, fmt.Errorf("解析成员清单失败 %s: %v", fullPath, err)
		}
		members = append(members, &WorkspaceMember{Manifest: m, Path: fullPath})
	}
	return members, nil
}

// ResolveWorkspace 求解整个工作区。
func ResolveWorkspace(ws *Workspace, registry Registry, policy CompatibilityPolicy) (*Resolution, error) {
	members, err := ws.LoadMembers()
	if err != nil {
		return nil, err
	}
	return ResolveMembers(members, registry, policy)
}

// ResolveMembers 求解一组工作区成员。
func ResolveMembers(members []*WorkspaceMember, registry Registry, policy CompatibilityPolicy) (*Resolution, error) {
	// 构建工作区包名集合
	workspaceNames := make(map[string]bool)
	for _, m := range members {
		workspaceNames[m.Manifest.Name] = true
	}

	// 创建包含工作区成员的复合 registry
	wsRegistry := NewWorkspaceRegistry(registry, members)

	// 收集所有根依赖边
	var roots []DependencyEdge
	for _, m := range members {
		source := Source{
			Kind: "workspace",
			Name: m.Manifest.Name,
			Path: m.Path,
		}
		for _, edge := range m.Manifest.Edges(source) {
			// 处理 workspace:* 引用
			if strings.HasPrefix(edge.Constraint, "workspace:") {
				if !workspaceNames[edge.Name] {
					return nil, &MissingDepError{
						Name:   edge.Name,
						Source: source,
					}
				}
				// workspace:* 引用解析为成员的精确版本
				member := findMemberByName(members, edge.Name)
				edge.Constraint = member.Manifest.Version
			}
			roots = append(roots, edge)
		}
	}

	solver := NewSolver(wsRegistry, policy)
	return solver.Solve(roots)
}

// WorkspaceRegistry 复合 registry：工作区成员优先，其次本地 registry。
type WorkspaceRegistry struct {
	base    Registry
	members map[string]*WorkspaceMember
}

// NewWorkspaceRegistry 创建工作区 registry。
func NewWorkspaceRegistry(base Registry, members []*WorkspaceMember) *WorkspaceRegistry {
	wr := &WorkspaceRegistry{
		base:    base,
		members: make(map[string]*WorkspaceMember),
	}
	for _, m := range members {
		wr.members[m.Manifest.Name] = m
	}
	return wr
}

// Versions 实现 Registry 接口。
func (wr *WorkspaceRegistry) Versions(name string) ([]Version, error) {
	if m, ok := wr.members[name]; ok {
		v, err := ParseVersion(m.Manifest.Version)
		if err != nil {
			return nil, err
		}
		return []Version{v}, nil
	}
	return wr.base.Versions(name)
}

// Manifest 实现 Registry 接口。
func (wr *WorkspaceRegistry) Manifest(name string, v Version) (*PackageManifest, error) {
	if m, ok := wr.members[name]; ok {
		mv, err := ParseVersion(m.Manifest.Version)
		if err != nil {
			return nil, err
		}
		if mv.Equal(v) {
			return &PackageManifest{
				Name:         m.Manifest.Name,
				Version:      m.Manifest.Version,
				Dependencies: m.Manifest.Dependencies,
			}, nil
		}
	}
	return wr.base.Manifest(name, v)
}

// Fingerprint 实现 Registry 接口。
func (wr *WorkspaceRegistry) Fingerprint() string {
	return wr.base.Fingerprint()
}

// findMemberByName 按名称查找成员。
func findMemberByName(members []*WorkspaceMember, name string) *WorkspaceMember {
	for _, m := range members {
		if m.Manifest.Name == name {
			return m
		}
	}
	return nil
}

// WorkspaceMembers 返回工作区成员名称列表（排序）。
func WorkspaceMembers(ws *Workspace) ([]string, error) {
	members, err := ws.LoadMembers()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, m := range members {
		names = append(names, m.Manifest.Name)
	}
	sort.Strings(names)
	return names, nil
}
