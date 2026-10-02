package resolver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PackageManifest 表示本地 registry 中一个包的某个版本的清单。
type PackageManifest struct {
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Dependencies map[string]string `json:"dependencies"`
}

// Registry 提供本地可用版本信息。
type Registry interface {
	// Versions 返回指定包的所有可用版本，按版本号升序排列。
	Versions(name string) ([]Version, error)
	// Manifest 返回指定包指定版本的清单。
	Manifest(name string, v Version) (*PackageManifest, error)
	// Fingerprint 返回 registry 的内容指纹，用于检测 registry 变更。
	Fingerprint() string
}

// MemoryRegistry 内存中的 registry 实现，主要用于测试。
type MemoryRegistry struct {
	packages  map[string][]Version
	manifests map[string]map[string]*PackageManifest
}

// NewMemoryRegistry 创建内存 registry。
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		packages:  make(map[string][]Version),
		manifests: make(map[string]map[string]*PackageManifest),
	}
}

// AddPackage 向 registry 添加一个包的版本及其清单。
func (r *MemoryRegistry) AddPackage(name string, m *PackageManifest) error {
	v, err := ParseVersion(m.Version)
	if err != nil {
		return fmt.Errorf("添加包 %q 失败: %v", name, err)
	}
	if r.manifests[name] == nil {
		r.manifests[name] = make(map[string]*PackageManifest)
	}
	key := v.String()
	if _, exists := r.manifests[name][key]; !exists {
		r.packages[name] = append(r.packages[name], v)
		sort.Slice(r.packages[name], func(i, j int) bool {
			return r.packages[name][i].Less(r.packages[name][j])
		})
	}
	r.manifests[name][key] = m
	return nil
}

// Versions 实现 Registry 接口。
func (r *MemoryRegistry) Versions(name string) ([]Version, error) {
	vs := r.packages[name]
	if len(vs) == 0 {
		return nil, nil
	}
	out := make([]Version, len(vs))
	copy(out, vs)
	return out, nil
}

// Manifest 实现 Registry 接口。
func (r *MemoryRegistry) Manifest(name string, v Version) (*PackageManifest, error) {
	m := r.manifests[name][v.String()]
	if m == nil {
		return nil, fmt.Errorf("包 %q@%s 不存在", name, v.String())
	}
	return m, nil
}

// Fingerprint 实现 Registry 接口。
func (r *MemoryRegistry) Fingerprint() string {
	var names []string
	for n := range r.manifests {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		vs := r.packages[n]
		for _, v := range vs {
			m := r.manifests[n][v.String()]
			data, _ := json.Marshal(m)
			h.Write([]byte(n))
			h.Write(data)
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// FileRegistry 基于本地目录的 registry。
// 目录结构: <root>/<name>/<version>.json
type FileRegistry struct {
	root string
}

// NewFileRegistry 创建基于目录的 registry。
func NewFileRegistry(root string) *FileRegistry {
	return &FileRegistry{root: root}
}

// Versions 实现 Registry 接口。
func (r *FileRegistry) Versions(name string) ([]Version, error) {
	dir := filepath.Join(r.root, filepath.FromSlash(name))
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var versions []Version
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		verStr := strings.TrimSuffix(e.Name(), ".json")
		v, err := ParseVersion(verStr)
		if err != nil {
			continue // 跳过非法版本文件
		}
		versions = append(versions, v)
	}
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Less(versions[j])
	})
	return versions, nil
}

// Manifest 实现 Registry 接口。
func (r *FileRegistry) Manifest(name string, v Version) (*PackageManifest, error) {
	path := filepath.Join(r.root, filepath.FromSlash(name), v.String()+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取包清单失败 %s: %v", path, err)
	}
	var m PackageManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("解析包清单失败 %s: %v", path, err)
	}
	return &m, nil
}

// Fingerprint 实现 Registry 接口。
func (r *FileRegistry) Fingerprint() string {
	h := sha256.New()
	_ = filepath.Walk(r.root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		h.Write(data)
		return nil
	})
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
