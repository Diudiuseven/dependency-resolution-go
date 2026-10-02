package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// buildFullRegistry 构建一个完整的测试 registry。
func buildFullRegistry() *MemoryRegistry {
	r := NewMemoryRegistry()
	// foo: 1.0.0, 1.5.0, 2.0.0
	r.AddPackage("foo", &PackageManifest{Name: "foo", Version: "1.0.0", Dependencies: map[string]string{"bar": "^1.0.0"}})
	r.AddPackage("foo", &PackageManifest{Name: "foo", Version: "1.5.0", Dependencies: map[string]string{"bar": "^1.5.0", "baz": "^1.0.0"}})
	r.AddPackage("foo", &PackageManifest{Name: "foo", Version: "2.0.0", Dependencies: map[string]string{"bar": "^2.0.0"}})
	// bar: 1.0.0, 1.5.0, 2.0.0
	r.AddPackage("bar", &PackageManifest{Name: "bar", Version: "1.0.0", Dependencies: map[string]string{"qux": "^1.0.0"}})
	r.AddPackage("bar", &PackageManifest{Name: "bar", Version: "1.5.0", Dependencies: map[string]string{"qux": "^1.5.0"}})
	r.AddPackage("bar", &PackageManifest{Name: "bar", Version: "2.0.0", Dependencies: map[string]string{"qux": "^2.0.0"}})
	// baz: 1.0.0
	r.AddPackage("baz", &PackageManifest{Name: "baz", Version: "1.0.0"})
	// qux: 1.0.0, 1.5.0, 2.0.0
	r.AddPackage("qux", &PackageManifest{Name: "qux", Version: "1.0.0"})
	r.AddPackage("qux", &PackageManifest{Name: "qux", Version: "1.5.0"})
	r.AddPackage("qux", &PackageManifest{Name: "qux", Version: "2.0.0"})
	return r
}

func TestDeterminism(t *testing.T) {
	r := buildFullRegistry()
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
		{Source: Source{Kind: "root", Name: "app"}, Name: "bar", Constraint: "^1.0.0"},
	}

	// 多次求解，结果必须一致
	var results []string
	for i := 0; i < 5; i++ {
		s := NewSolver(r, PolicyMajor)
		res, err := s.Solve(roots)
		if err != nil {
			t.Fatalf("求解失败: %v", err)
		}
		results = append(results, resolutionFingerprint(res))
	}
	for i := 1; i < len(results); i++ {
		if results[i] != results[0] {
			t.Fatalf("结果不一致: %s != %s", results[i], results[0])
		}
	}

	// 打乱 roots 顺序，结果仍须一致
	shuffled := []DependencyEdge{roots[1], roots[0]}
	s := NewSolver(r, PolicyMajor)
	res, err := s.Solve(shuffled)
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	if resolutionFingerprint(res) != results[0] {
		t.Fatalf("打乱顺序后结果不一致")
	}
}

func resolutionFingerprint(res *Resolution) string {
	var parts []string
	for _, slot := range res.Slots {
		if slot.Selected != nil {
			parts = append(parts, fmt.Sprintf("%s@%s[%s]", slot.Key.Name, slot.Selected.String(), slot.Key.Boundary))
		}
	}
	sort.Strings(parts)
	return fmt.Sprintf("%v", parts)
}

func TestConflictExplanation(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "~1.6.0"},
	}
	_, err := s.Solve(roots)
	if err == nil {
		t.Fatal("应该返回冲突错误")
	}
	conflict, ok := err.(*ConflictError)
	if !ok {
		t.Fatalf("应该是 ConflictError, 实际 %T", err)
	}
	msg := ExplainConflict(conflict)
	fmt.Printf("冲突说明:\n%s\n", msg)
	if len(conflict.Constraints) < 2 {
		t.Fatal("冲突说明应包含至少两个约束来源")
	}
}

func TestMissingDep(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "nonexistent", Constraint: "^1.0.0"},
	}
	_, err := s.Solve(roots)
	if err == nil {
		t.Fatal("应该返回依赖缺失错误")
	}
	missing, ok := err.(*MissingDepError)
	if !ok {
		t.Fatalf("应该是 MissingDepError, 实际 %T", err)
	}
	fmt.Printf("缺失说明: %s\n", ExplainMissingDep(missing))
}

func TestInvalidRange(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "not-a-version"},
	}
	_, err := s.Solve(roots)
	if err == nil {
		t.Fatal("应该返回非法范围错误")
	}
	invalid, ok := err.(*InvalidRangeError)
	if !ok {
		t.Fatalf("应该是 InvalidRangeError, 实际 %T", err)
	}
	fmt.Printf("非法范围说明: %s\n", ExplainInvalidRange(invalid))
}

func TestCycle(t *testing.T) {
	r := NewMemoryRegistry()
	// A 依赖 B，B 依赖 A，形成环
	r.AddPackage("a", &PackageManifest{Name: "a", Version: "1.0.0", Dependencies: map[string]string{"b": "^1.0.0"}})
	r.AddPackage("b", &PackageManifest{Name: "b", Version: "1.0.0", Dependencies: map[string]string{"a": "^1.0.0"}})
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "a", Constraint: "^1.0.0"},
	}
	_, err := s.Solve(roots)
	if err == nil {
		t.Fatal("应该返回环错误")
	}
	cycle, ok := err.(*CycleError)
	if !ok {
		t.Fatalf("应该是 CycleError, 实际 %T", err)
	}
	fmt.Printf("环说明: %s\n", ExplainCycle(cycle))
}

func TestLockAndInstall(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	}
	res, err := s.Solve(roots)
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}

	lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
	if err != nil {
		t.Fatalf("生成锁定文件失败: %v", err)
	}

	// 写入并读取
	tmpDir, err := os.MkdirTemp("", "depres-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)
	lockPath := filepath.Join(tmpDir, "depres-lock.json")
	if err := WriteLock(lockPath, lock); err != nil {
		t.Fatalf("写入锁定文件失败: %v", err)
	}
	readLock, err := ReadLock(lockPath)
	if err != nil {
		t.Fatalf("读取锁定文件失败: %v", err)
	}
	if readLock.Format != LockFormat {
		t.Fatalf("锁定文件格式错误: %s", readLock.Format)
	}

	// 安装
	plan, err := Install(readLock, r, filepath.Join(tmpDir, "node_modules"))
	if err != nil {
		t.Fatalf("安装失败: %v", err)
	}
	if len(plan.Slots) == 0 {
		t.Fatal("安装计划不应为空")
	}
	fmt.Printf("安装了 %d 个包\n", len(plan.Slots))
}

func TestVerifyMissing(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	}
	res, err := s.Solve(roots)
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
	if err != nil {
		t.Fatalf("生成锁定文件失败: %v", err)
	}

	// 添加一个新的依赖约束，锁定文件中没有满足的槽位
	// foo ^2.0.0 在锁定文件中只有 foo 1.5.0，不满足
	manifests := []*ModuleManifest{
		{
			Name: "app",
			Dependencies: map[string]string{
				"foo": "^2.0.0",
			},
		},
	}
	result, err := Verify(manifests, lock, r)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if len(result.Missing) == 0 {
		t.Fatal("应该检测到缺项")
	}
	fmt.Printf("校验结果:\n%s\n", result.Summary())
}

func TestVerifyDrift(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	}
	res, err := s.Solve(roots)
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
	if err != nil {
		t.Fatalf("生成锁定文件失败: %v", err)
	}

	// 修改 registry 中 foo 1.5.0 的清单（添加新依赖），造成漂移
	r2 := NewMemoryRegistry()
	// 复制原有包
	for _, name := range []string{"foo", "bar", "baz", "qux"} {
		vs, _ := r.Versions(name)
		for _, v := range vs {
			m, _ := r.Manifest(name, v)
			r2.AddPackage(name, m)
		}
	}
	// 修改 foo 1.5.0 的清单
	r2.AddPackage("foo", &PackageManifest{Name: "foo", Version: "1.5.0", Dependencies: map[string]string{"bar": "^1.5.0", "baz": "^1.0.0", "qux": "^1.0.0"}})

	manifests := []*ModuleManifest{
		{Name: "app", Dependencies: map[string]string{"foo": "^1.0.0"}},
	}
	result, err := Verify(manifests, lock, r2)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if len(result.Drift) == 0 {
		t.Fatal("应该检测到漂移")
	}
	fmt.Printf("漂移检测:\n%s\n", result.Summary())
}

func TestIncremental(t *testing.T) {
	r := buildFullRegistry()
	inc := NewIncrementalResolver(r, PolicyMajor)

	// 全量求解
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	}
	fullRes, err := inc.Resolve(roots)
	if err != nil {
		t.Fatalf("全量求解失败: %v", err)
	}

	// 增量求解（相同输入）
	incRes, err := inc.ResolveIncremental(context.Background(), roots, fullRes)
	if err != nil {
		t.Fatalf("增量求解失败: %v", err)
	}
	if resolutionFingerprint(incRes) != resolutionFingerprint(fullRes) {
		t.Fatalf("增量结果与全量结果不一致:\n增量: %s\n全量: %s", resolutionFingerprint(incRes), resolutionFingerprint(fullRes))
	}

	// 小幅变化：添加一个依赖
	newRoots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
		{Source: Source{Kind: "root", Name: "app"}, Name: "bar", Constraint: "^1.0.0"},
	}
	incRes2, err := inc.ResolveIncremental(context.Background(), newRoots, fullRes)
	if err != nil {
		t.Fatalf("增量求解失败: %v", err)
	}
	// 验证增量结果与全量重算一致
	fullRes2, err := inc.Resolve(newRoots)
	if err != nil {
		t.Fatalf("全量重算失败: %v", err)
	}
	if resolutionFingerprint(incRes2) != resolutionFingerprint(fullRes2) {
		t.Fatalf("增量结果与全量重算不一致:\n增量: %s\n全量: %s", resolutionFingerprint(incRes2), resolutionFingerprint(fullRes2))
	}
	fmt.Printf("增量求解验证通过\n")
}

func TestCancellation(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	}
	_, err := s.SolveContext(ctx, roots)
	if err == nil {
		t.Fatal("应该返回取消错误")
	}
	fmt.Printf("取消错误: %v\n", err)
}

func TestWorkspace(t *testing.T) {
	r := buildFullRegistry()
	// 创建工作区
	tmpDir, err := os.MkdirTemp("", "depres-ws-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 成员 app 依赖 foo（不引用 lib）
	appDir := filepath.Join(tmpDir, "app")
	os.MkdirAll(appDir, 0755)
	appManifest := &ModuleManifest{
		Name:    "app",
		Version: "1.0.0",
		Dependencies: map[string]string{
			"foo": "^1.0.0",
		},
	}
	writeManifest(t, filepath.Join(appDir, "package.json"), appManifest)

	// 成员 lib 依赖 bar（独立成员，不被 app 引用）
	libDir := filepath.Join(tmpDir, "lib")
	os.MkdirAll(libDir, 0755)
	libManifest := &ModuleManifest{
		Name:    "lib",
		Version: "1.0.0",
		Dependencies: map[string]string{
			"bar": "^1.0.0",
		},
	}
	writeManifest(t, filepath.Join(libDir, "package.json"), libManifest)

	// 工作区配置（包含两个成员）
	wsPath := filepath.Join(tmpDir, "depres-workspace.json")
	wsData := fmt.Sprintf(`{"members": ["%s", "%s"]}`, filepath.Join("app", "package.json"), filepath.Join("lib", "package.json"))
	os.WriteFile(wsPath, []byte(wsData), 0644)

	ws, err := LoadWorkspace(wsPath)
	if err != nil {
		t.Fatalf("加载工作区失败: %v", err)
	}
	res, err := ResolveWorkspace(ws, r, PolicyMajor)
	if err != nil {
		t.Fatalf("工作区求解失败: %v", err)
	}
	fp := resolutionFingerprint(res)
	fmt.Printf("工作区求解结果: %s\n", fp)

	// 移除 lib 成员，app 的结论不应改变
	ws2 := &Workspace{Members: []string{filepath.Join("app", "package.json")}, root: tmpDir}
	res2, err := ResolveWorkspace(ws2, r, PolicyMajor)
	if err != nil {
		t.Fatalf("移除成员后求解失败: %v", err)
	}
	fp2 := resolutionFingerprint(res2)
	fmt.Printf("移除成员后结果: %s\n", fp2)

	// 验证 app 的结论不变（foo 的版本和边界相同）
	fooBefore := findSlot(res, "foo", "1")
	fooAfter := findSlot(res2, "foo", "1")
	if fooBefore == nil || fooAfter == nil {
		t.Fatal("foo 槽位不应消失")
	}
	if fooBefore.Selected.String() != fooAfter.Selected.String() {
		t.Fatalf("移除成员后 foo 版本改变: %s -> %s", fooBefore.Selected.String(), fooAfter.Selected.String())
	}
	fmt.Printf("移除成员后 app 的结论不变: foo@%s\n", fooAfter.Selected.String())
}

func TestPolicyConfigurability(t *testing.T) {
	r := buildFullRegistry()
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	}

	for _, policy := range []CompatibilityPolicy{PolicyMajor, PolicyMinor, PolicyExact, PolicyUnified} {
		s := NewSolver(r, policy)
		res, err := s.Solve(roots)
		if err != nil {
			t.Fatalf("策略 %s 求解失败: %v", policy.Name(), err)
		}
		fp := resolutionFingerprint(res)
		fmt.Printf("策略 %s: %s\n", policy.Name(), fp)
	}
}

func writeManifest(t *testing.T, path string, m *ModuleManifest) {
	t.Helper()
	data, err := jsonMarshalIndent(m)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, data, 0644)
}

func jsonMarshalIndent(m *ModuleManifest) ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

func TestDiamondDependency(t *testing.T) {
	// 钻石依赖：A 依赖 B 和 C，B 依赖 D，C 依赖 D
	r := NewMemoryRegistry()
	r.AddPackage("a", &PackageManifest{Name: "a", Version: "1.0.0", Dependencies: map[string]string{"b": "^1.0.0", "c": "^1.0.0"}})
	r.AddPackage("b", &PackageManifest{Name: "b", Version: "1.0.0", Dependencies: map[string]string{"d": "^1.0.0"}})
	r.AddPackage("c", &PackageManifest{Name: "c", Version: "1.0.0", Dependencies: map[string]string{"d": "^1.0.0"}})
	r.AddPackage("d", &PackageManifest{Name: "d", Version: "1.0.0"})
	r.AddPackage("d", &PackageManifest{Name: "d", Version: "1.5.0"})
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "a", Constraint: "^1.0.0"},
	}
	res, err := s.Solve(roots)
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	// d 应该只有一个槽位（1.x），版本为 1.5.0
	dSlot := findSlot(res, "d", "1")
	if dSlot == nil {
		t.Fatal("d 应该有一个槽位")
	}
	if dSlot.Selected.String() != "1.5.0" {
		t.Fatalf("d 版本应该是 1.5.0, 实际 %s", dSlot.Selected.String())
	}
	fmt.Printf("钻石依赖求解通过: d@%s\n", dSlot.Selected.String())
}

func TestLargeScaleDeterminism(t *testing.T) {
	// 大规模测试：50 个包，每个包依赖索引更大的包（无环）
	r := NewMemoryRegistry()
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("pkg%d", i)
		deps := make(map[string]string)
		for j := 1; j <= 3; j++ {
			depIdx := i + j
			if depIdx < 50 {
				deps[fmt.Sprintf("pkg%d", depIdx)] = "^1.0.0"
			}
		}
		r.AddPackage(name, &PackageManifest{Name: name, Version: "1.0.0", Dependencies: deps})
		r.AddPackage(name, &PackageManifest{Name: name, Version: "1.5.0", Dependencies: deps})
	}

	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "pkg0", Constraint: "^1.0.0"},
	}

	// 多次求解，结果必须一致
	var fps []string
	for i := 0; i < 3; i++ {
		s := NewSolver(r, PolicyMajor)
		res, err := s.Solve(roots)
		if err != nil {
			t.Fatalf("求解失败: %v", err)
		}
		fps = append(fps, resolutionFingerprint(res))
	}
	for i := 1; i < len(fps); i++ {
		if fps[i] != fps[0] {
			t.Fatalf("大规模求解结果不一致")
		}
	}
	fmt.Printf("大规模确定性测试通过: %d 个槽位\n", len(fps[0]))
}

func TestConstraintParsing(t *testing.T) {
	tests := []struct {
		constraint string
		version    string
		expected   bool
	}{
		{"^1.2.3", "1.2.3", true},
		{"^1.2.3", "1.9.9", true},
		{"^1.2.3", "2.0.0", false},
		{"~1.2.3", "1.2.9", true},
		{"~1.2.3", "1.3.0", false},
		{">=1.0.0 <2.0.0", "1.5.0", true},
		{">=1.0.0 <2.0.0", "2.0.0", false},
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "1.2.4", false},
		{"*", "9.9.9", true},
		{"1.x", "1.5.0", true},
		{"1.x", "2.0.0", false},
		{"1.2.x", "1.2.5", true},
		{"1.2.x", "1.3.0", false},
		{"^0.2.3", "0.2.9", true},
		{"^0.2.3", "0.3.0", false},
		{"^0.0.3", "0.0.3", true},
		{"^0.0.3", "0.0.4", false},
	}
	for _, tt := range tests {
		c, err := ParseConstraint(tt.constraint)
		if err != nil {
			t.Errorf("解析 %q 失败: %v", tt.constraint, err)
			continue
		}
		v := MustParseVersion(tt.version)
		if c.Satisfies(v) != tt.expected {
			t.Errorf("约束 %q 对版本 %s 的满足性: 期望 %v, 实际 %v", tt.constraint, tt.version, tt.expected, c.Satisfies(v))
		}
	}
}

func TestLockUpdatePolicy(t *testing.T) {
	r := buildFullRegistry()
	s := NewSolver(r, PolicyMajor)
	roots := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	}
	res, err := s.Solve(roots)
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
	if err != nil {
		t.Fatalf("生成锁定文件失败: %v", err)
	}

	// 验证无漂移
	manifests := []*ModuleManifest{
		{Name: "app", Dependencies: map[string]string{"foo": "^1.0.0"}},
	}
	result, err := Verify(manifests, lock, r)
	if err != nil {
		t.Fatalf("校验失败: %v", err)
	}
	if result.HasIssues() {
		t.Fatalf("不应有问题: %s", result.Summary())
	}
	fmt.Printf("锁定更新策略测试通过\n")
}

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		a, b     string
		expected int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.0.0", 1},
		{"1.2.3", "1.2.4", -1},
		{"1.0.0-alpha", "1.0.0", -1},
		{"1.0.0", "1.0.0-alpha", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
	}
	for _, tt := range tests {
		a := MustParseVersion(tt.a)
		b := MustParseVersion(tt.b)
		if a.Compare(b) != tt.expected {
			t.Errorf("比较 %s vs %s: 期望 %d, 实际 %d", tt.a, tt.b, tt.expected, a.Compare(b))
		}
	}
}

func findSlot(res *Resolution, name, boundary string) *Slot {
	for _, slot := range res.Slots {
		if slot.Key.Name == name && slot.Key.Boundary == boundary {
			return slot
		}
	}
	return nil
}
