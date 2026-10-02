package resolver

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ModuleManifest 表示一个模块（或 workspace 成员）的依赖清单。
type ModuleManifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
}

// DependencyEdge 表示一条依赖边：来源 -> 依赖名 + 约束。
type DependencyEdge struct {
	Source     Source
	Name       string
	Constraint string
}

// Source 表示依赖的来源，用于解释冲突。
type Source struct {
	Kind string // "root", "workspace", "package"
	Name string // 模块名或包名
	Path string // 文件路径（可选）
}

// ParseModuleManifest 从 JSON 数据解析模块清单。
func ParseModuleManifest(data []byte) (*ModuleManifest, error) {
	var m ModuleManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析模块清单失败: %v", err)
	}
	if m.Name == "" {
		return nil, fmt.Errorf("模块清单缺少 name 字段")
	}
	return &m, nil
}

// Edges 返回清单中声明的所有依赖边，按依赖名排序以保证确定性。
func (m *ModuleManifest) Edges(source Source) []DependencyEdge {
	var names []string
	for n := range m.Dependencies {
		names = append(names, n)
	}
	sort.Strings(names)
	edges := make([]DependencyEdge, 0, len(names))
	for _, n := range names {
		edges = append(edges, DependencyEdge{
			Source:     source,
			Name:       n,
			Constraint: m.Dependencies[n],
		})
	}
	return edges
}

// SourceString 返回来源的可读描述。
func (s Source) String() string {
	if s.Path != "" {
		return fmt.Sprintf("%s(%s)", s.Name, s.Path)
	}
	return s.Name
}
