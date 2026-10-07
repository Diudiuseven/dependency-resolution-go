package resolver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// buildBacktrackRegistry 构造一个必然触发回溯残留槽位的 registry：
// x 1.5.0 -> e 1.0.0 -> g ^2.0.0（g 仅有 1.0.0，冲突），
// 求解回溯到 x 1.0.0（仅依赖 d）后成功，但 e/1 槽位以 selected=nil 残留在结果中。
func buildBacktrackRegistry() *MemoryRegistry {
	r := NewMemoryRegistry()
	r.AddPackage("x", &PackageManifest{Name: "x", Version: "1.5.0", Dependencies: map[string]string{"d": "^1.0.0", "e": "^1.0.0"}})
	r.AddPackage("x", &PackageManifest{Name: "x", Version: "1.0.0", Dependencies: map[string]string{"d": "^1.0.0"}})
	r.AddPackage("d", &PackageManifest{Name: "d", Version: "1.0.0"})
	r.AddPackage("e", &PackageManifest{Name: "e", Version: "1.0.0", Dependencies: map[string]string{"g": "^2.0.0"}})
	r.AddPackage("g", &PackageManifest{Name: "g", Version: "1.0.0"})
	return r
}

func backtrackRoots() []DependencyEdge {
	return []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "x", Constraint: "^1.0.0"},
	}
}

// TestExplainAbandonedSlotDoesNotCrash 覆盖会触发崩溃的那类清单：
// 求解成功但结果中存在未选定版本的槽位时，说明输出必须完整可读，不得 panic。
func TestExplainAbandonedSlotDoesNotCrash(t *testing.T) {
	r := buildBacktrackRegistry()
	s := NewSolver(r, PolicyMajor)
	res, err := s.Solve(backtrackRoots())
	if err != nil {
		t.Fatalf("求解本应成功: %v", err)
	}

	// 确认夹具确实制造了未选定版本的槽位，否则本测试失去意义
	foundUnresolved := false
	for _, slot := range res.Slots {
		if slot.Selected == nil {
			foundUnresolved = true
		}
	}
	if !foundUnresolved {
		t.Fatal("测试夹具应当在结果中留下未选定版本的槽位")
	}

	var out string
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				t.Fatalf("说明输出发生崩溃: %v", rec)
			}
		}()
		out = ExplainResolution(res)
	}()

	if !strings.Contains(out, "求解结果") {
		t.Fatalf("说明缺少求解结论标题:\n%s", out)
	}
	if !strings.Contains(out, "x") || !strings.Contains(out, "1.0.0") {
		t.Fatalf("说明缺少成功求解的结论 x@1.0.0:\n%s", out)
	}
	if !strings.Contains(out, "e") {
		t.Fatalf("说明必须如实呈现未落到具体版本的依赖 e:\n%s", out)
	}
	if !strings.Contains(out, "未") {
		t.Fatalf("说明必须标明未选定版本的槽位:\n%s", out)
	}
	t.Logf("说明输出:\n%s", out)
}

// TestExplainDeterministic 同一份结论的说明输出必须逐字节一致。
func TestExplainDeterministic(t *testing.T) {
	r := buildBacktrackRegistry()
	s1 := NewSolver(r, PolicyMajor)
	res1, err := s1.Solve(backtrackRoots())
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	s2 := NewSolver(r, PolicyMajor)
	res2, err := s2.Solve(backtrackRoots())
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	if ExplainResolution(res1) != ExplainResolution(res2) {
		t.Fatalf("说明输出不确定:\n%s\n---\n%s", ExplainResolution(res1), ExplainResolution(res2))
	}
}

// TestLockByteReproducible 相同输入连续两次生成的锁定结果必须逐字节一致，
// 即使两次生成跨越了运行时刻（旧实现的 resolved_at 时间戳会在此暴露差异）。
func TestLockByteReproducible(t *testing.T) {
	r := buildBacktrackRegistry()
	gen := func() []byte {
		s := NewSolver(r, PolicyMajor)
		res, err := s.Solve(backtrackRoots())
		if err != nil {
			t.Fatalf("求解失败: %v", err)
		}
		lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
		if err != nil {
			t.Fatalf("生成锁定结果失败: %v", err)
		}
		data, err := json.MarshalIndent(lock, "", "  ")
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		return data
	}
	first := gen()
	time.Sleep(1100 * time.Millisecond) // 跨过秒级时间戳边界
	second := gen()
	if string(first) != string(second) {
		t.Fatalf("锁定结果不可复现:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(string(first), "resolved_at") {
		t.Fatalf("锁定结果不得包含随运行时刻变化的 resolved_at 字段:\n%s", first)
	}
}

// TestLockConcurrentReproducible 并发多次生成锁定结果，逐字节必须一致。
func TestLockConcurrentReproducible(t *testing.T) {
	r := buildBacktrackRegistry()
	roots := backtrackRoots()

	base := func() []byte {
		s := NewSolver(r, PolicyMajor)
		res, err := s.Solve(roots)
		if err != nil {
			t.Fatalf("求解失败: %v", err)
		}
		lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
		if err != nil {
			t.Fatalf("生成锁定结果失败: %v", err)
		}
		data, err := json.MarshalIndent(lock, "", "  ")
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		return data
	}
	want := base()

	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s := NewSolver(r, PolicyMajor)
			res, err := s.Solve(roots)
			if err != nil {
				errs <- fmt.Errorf("求解失败: %w", err)
				return
			}
			lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
			if err != nil {
				errs <- fmt.Errorf("生成锁定结果失败: %w", err)
				return
			}
			got, err := json.MarshalIndent(lock, "", "  ")
			if err != nil {
				errs <- fmt.Errorf("序列化失败: %w", err)
				return
			}
			if !bytes.Equal(got, want) {
				errs <- fmt.Errorf("并发输出与基准不一致")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

// TestLockShuffledManifestOrder 打乱清单声明顺序后，锁定结果仍须逐字节一致。
func TestLockShuffledManifestOrder(t *testing.T) {
	r := buildFullRegistry()
	ordered := []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "bar", Constraint: "^1.0.0"},
		{Source: Source{Kind: "root", Name: "app"}, Name: "baz", Constraint: "^1.0.0"},
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
		{Source: Source{Kind: "root", Name: "app"}, Name: "qux", Constraint: "^1.0.0"},
	}
	shuffled := []DependencyEdge{ordered[2], ordered[0], ordered[3], ordered[1]}

	gen := func(edges []DependencyEdge) []byte {
		s := NewSolver(r, PolicyMajor)
		res, err := s.Solve(edges)
		if err != nil {
			t.Fatalf("求解失败: %v", err)
		}
		lock, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
		if err != nil {
			t.Fatalf("生成锁定结果失败: %v", err)
		}
		data, err := json.MarshalIndent(lock, "", "  ")
		if err != nil {
			t.Fatalf("序列化失败: %v", err)
		}
		return data
	}
	first := gen(ordered)
	second := gen(shuffled)
	if !bytes.Equal(first, second) {
		t.Fatalf("打乱清单顺序后锁定结果不一致:\n%s\n---\n%s", first, second)
	}
}

// TestLegacyV1LockCompatible 旧的 depres-lock/v1 锁定结果必须能继续读取、校验和安装，
// 并给出可区分的兼容提示。
func TestLegacyV1LockCompatible(t *testing.T) {
	r := buildFullRegistry()
	// 手工构造一份旧格式（含 resolved_at、无 unresolved_slots）。
	legacy := `{
  "format": "depres-lock/v1",
  "resolved_at": "2026-10-01T00:00:00Z",
  "registry_fingerprint": "` + r.Fingerprint() + `",
  "policy": "major",
  "members": ["app"],
  "slots": []
}`
	// 用真实求解结果补齐 slots 的 v1 形态，确保后续 Install/Verify 可用。
	s := NewSolver(r, PolicyMajor)
	res, err := s.Solve([]DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
	})
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	v2, err := LockFromResolution(res, r, PolicyMajor, []string{"app"})
	if err != nil {
		t.Fatalf("生成锁定结果失败: %v", err)
	}
	slotsJSON, err := json.Marshal(v2.Slots)
	if err != nil {
		t.Fatal(err)
	}
	legacy = `{
  "format": "depres-lock/v1",
  "resolved_at": "2026-10-01T00:00:00Z",
  "registry_fingerprint": "` + r.Fingerprint() + `",
  "policy": "major",
  "members": ["app"],
  "slots": ` + string(slotsJSON) + `
}`

	lock, notices, err := parseLock([]byte(legacy))
	if err != nil {
		t.Fatalf("旧锁定结果应能兼容读取: %v", err)
	}

	kinds := map[CompatNoticeKind]bool{}
	for _, n := range notices {
		kinds[n.Kind] = true
	}
	if !kinds[CompatNoticeV1Format] {
		t.Fatalf("应提示旧格式，实际提示: %v", notices)
	}
	if !kinds[CompatNoticeResolvedAtRemoved] {
		t.Fatalf("应提示 resolved_at 已移除，实际提示: %v", notices)
	}

	// 旧结果必须仍可校验且无缺项/漂移。
	manifests := []*ModuleManifest{
		{Name: "app", Dependencies: map[string]string{"foo": "^1.0.0"}},
	}
	verify, err := Verify(manifests, lock, r)
	if err != nil {
		t.Fatalf("旧锁定结果校验失败: %v", err)
	}
	if verify.HasIssues() {
		t.Fatalf("旧锁定结果不应有缺项/漂移: %s", verify.Summary())
	}

	// 旧结果必须仍可安装。
	plan, err := Install(lock, r, t.TempDir())
	if err != nil {
		t.Fatalf("旧锁定结果安装失败: %v", err)
	}
	if len(plan.Slots) == 0 {
		t.Fatal("安装计划不应为空")
	}
}

// TestLegacyV1UnknownFieldReported v2 文件中的未知字段必须产生可区分提示且不影响读取。
func TestLegacyV1UnknownFieldReported(t *testing.T) {
	doc := `{
  "format": "depres-lock/v2",
  "registry_fingerprint": "sha256:abc",
  "policy": "major",
  "members": [],
  "slots": [],
  "unresolved_slots": [],
  "experimental": true
}`
	lock, notices, err := parseLock([]byte(doc))
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(lock.Slots) != 0 {
		t.Fatalf("槽位应为空")
	}
	found := false
	for _, n := range notices {
		if n.Kind == CompatNoticeUnknownField && n.Field == "experimental" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应报告未知字段 experimental，实际: %v", notices)
	}
}
