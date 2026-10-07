package resolver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// marshalLock 以与 WriteLock 相同的方式序列化锁定结果。
func marshalLock(lock *Lock) ([]byte, error) {
	return json.MarshalIndent(lock, "", "  ")
}

// lockTestRegistry 构建用于锁定结果测试的 registry（含未选定槽位场景）。
func lockTestRegistry() *MemoryRegistry {
	r := NewMemoryRegistry()
	r.AddPackage("lib", &PackageManifest{Name: "lib", Version: "1.0.0"})
	r.AddPackage("lib", &PackageManifest{Name: "lib", Version: "2.0.0", Dependencies: map[string]string{"dep": "^2.0.0"}})
	r.AddPackage("dep", &PackageManifest{Name: "dep", Version: "1.0.0"})
	r.AddPackage("foo", &PackageManifest{Name: "foo", Version: "1.0.0", Dependencies: map[string]string{"dep": "^1.0.0"}})
	r.AddPackage("foo", &PackageManifest{Name: "foo", Version: "1.5.0", Dependencies: map[string]string{"dep": "^1.0.0", "lib": "^1.0.0"}})
	return r
}

// lockTestRoots 返回测试用根依赖边。
func lockTestRoots() []DependencyEdge {
	return []DependencyEdge{
		{Source: Source{Kind: "root", Name: "app"}, Name: "foo", Constraint: "^1.0.0"},
		{Source: Source{Kind: "root", Name: "app"}, Name: "lib", Constraint: ">=1.0.0"},
	}
}

// solveAndLockBytes 求解并返回锁定文件的序列化字节。
func solveAndLockBytes(t *testing.T, roots []DependencyEdge, members []string) []byte {
	t.Helper()
	registry := lockTestRegistry()
	s := NewSolver(registry, PolicyMajor)
	res, err := s.Solve(roots)
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	lock, err := LockFromResolution(res, registry, PolicyMajor, members)
	if err != nil {
		t.Fatalf("生成锁定结果失败: %v", err)
	}
	data, err := marshalLock(lock)
	if err != nil {
		t.Fatalf("序列化锁定结果失败: %v", err)
	}
	return data
}

// TestLockDeterministicRepeated 相同输入多次求解，锁定结果必须逐字节一致。
func TestLockDeterministicRepeated(t *testing.T) {
	base := solveAndLockBytes(t, lockTestRoots(), []string{"app"})
	for i := 0; i < 20; i++ {
		got := solveAndLockBytes(t, lockTestRoots(), []string{"app"})
		if !bytes.Equal(got, base) {
			t.Fatalf("第 %d 次运行锁定结果不一致:\n%s\n---\n%s", i, got, base)
		}
	}
	if bytes.Contains(base, []byte("resolved_at")) {
		t.Fatal("锁定结果不应包含随运行时刻变化的字段 resolved_at")
	}
}

// TestLockDeterministicConcurrent 并发求解，锁定结果必须逐字节一致。
func TestLockDeterministicConcurrent(t *testing.T) {
	base := solveAndLockBytes(t, lockTestRoots(), []string{"app"})
	const n = 16
	results := make([][]byte, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			registry := lockTestRegistry()
			s := NewSolver(registry, PolicyMajor)
			res, err := s.Solve(lockTestRoots())
			if err != nil {
				t.Errorf("并发求解失败: %v", err)
				return
			}
			lock, err := LockFromResolution(res, registry, PolicyMajor, []string{"app"})
			if err != nil {
				t.Errorf("并发生成锁定结果失败: %v", err)
				return
			}
			data, err := marshalLock(lock)
			if err != nil {
				t.Errorf("并发序列化失败: %v", err)
				return
			}
			results[i] = data
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if !bytes.Equal(results[i], base) {
			t.Fatalf("并发运行 %d 的锁定结果不一致", i)
		}
	}
}

// TestLockDeterministicManifestOrder 清单声明顺序打乱后，锁定结果必须逐字节一致。
func TestLockDeterministicManifestOrder(t *testing.T) {
	roots := lockTestRoots()
	base := solveAndLockBytes(t, roots, []string{"app"})

	// 打乱根依赖边顺序
	shuffled := []DependencyEdge{roots[1], roots[0]}
	got := solveAndLockBytes(t, shuffled, []string{"app"})
	if !bytes.Equal(got, base) {
		t.Fatalf("打乱清单声明顺序后锁定结果不一致:\n%s\n---\n%s", got, base)
	}

	// 成员列表顺序打乱
	got = solveAndLockBytes(t, roots, []string{"zzz", "app", "mid"})
	got2 := solveAndLockBytes(t, roots, []string{"mid", "zzz", "app"})
	if !bytes.Equal(got, got2) {
		t.Fatalf("成员列表顺序打乱后锁定结果不一致:\n%s\n---\n%s", got, got2)
	}
}

// TestLockDeterministicAcrossProcesses 进程重启后，锁定结果必须逐字节一致。
// 通过子进程重新求解同一场景并输出锁定字节，与当前进程对比。
func TestLockDeterministicAcrossProcesses(t *testing.T) {
	base := solveAndLockBytes(t, lockTestRoots(), []string{"app"})
	for i := 0; i < 3; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess", "--")
		cmd.Env = append(os.Environ(), "GO_LOCK_HELPER_PROCESS=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("子进程运行失败: %v", err)
		}
		if !bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(base)) {
			t.Fatalf("第 %d 次子进程锁定结果不一致:\n%s\n---\n%s", i, out, base)
		}
	}
}

// TestLockHelperProcess 是子进程辅助测试，不是真正的测试用例。
func TestLockHelperProcess(t *testing.T) {
	if os.Getenv("GO_LOCK_HELPER_PROCESS") != "1" {
		return
	}
	registry := lockTestRegistry()
	s := NewSolver(registry, PolicyMajor)
	res, err := s.Solve(lockTestRoots())
	if err != nil {
		fmt.Fprintf(os.Stderr, "求解失败: %v\n", err)
		os.Exit(1)
	}
	lock, err := LockFromResolution(res, registry, PolicyMajor, []string{"app"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "生成锁定结果失败: %v\n", err)
		os.Exit(1)
	}
	data, err := marshalLock(lock)
	if err != nil {
		fmt.Fprintf(os.Stderr, "序列化失败: %v\n", err)
		os.Exit(1)
	}
	os.Stdout.Write(data)
	os.Exit(0)
}

// writeTempLock 将内容写入临时锁定文件。
func writeTempLock(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/depres-lock.json"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("写入临时锁定文件失败: %v", err)
	}
	return path
}

// oldLockJSON 是一份旧版本（含 resolved_at 字段）的锁定文件内容。
const oldLockJSON = `{
  "format": "depres-lock/v1",
  "resolved_at": "2024-01-15T08:30:00Z",
  "registry_fingerprint": "sha256:old",
  "policy": "major",
  "members": ["app"],
  "slots": [
    {
      "name": "dep",
      "boundary": "1",
      "version": "1.0.0",
      "integrity": "sha256:abc",
      "edges": []
    }
  ]
}`

// TestReadOldLockCompat 旧锁定结果（含已移除字段）必须能继续读取、校验和使用，
// 并给出可区分的兼容提示。
func TestReadOldLockCompat(t *testing.T) {
	path := writeTempLock(t, oldLockJSON)

	lock, warnings, err := ReadLockCompat(path)
	if err != nil {
		t.Fatalf("旧锁定文件应能继续读取: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("读取含已移除字段的旧锁定文件应给出兼容提示")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "resolved_at") {
			found = true
		}
	}
	if !found {
		t.Fatalf("兼容提示应明确指出已移除字段 resolved_at: %v", warnings)
	}

	// 内容必须完整可用
	if lock.Format != LockFormat {
		t.Fatalf("格式标识不符: %s", lock.Format)
	}
	if lock.Policy != "major" {
		t.Fatalf("策略不符: %s", lock.Policy)
	}
	slot := FindLockedSlot(lock, "dep", "1")
	if slot == nil {
		t.Fatal("旧锁定文件中的槽位应可查找")
	}
	if slot.Version != "1.0.0" || slot.Integrity != "sha256:abc" {
		t.Fatalf("旧锁定文件槽位内容不符: %+v", slot)
	}

	// 兼容旧签名: ReadLock 也必须能读取旧文件
	lock2, err := ReadLock(path)
	if err != nil {
		t.Fatalf("ReadLock 应能读取旧锁定文件: %v", err)
	}
	if FindLockedSlot(lock2, "dep", "1") == nil {
		t.Fatal("ReadLock 读取的旧锁定文件槽位应可查找")
	}
}

// TestReadNewLockNoWarnings 新格式锁定文件读取时不应产生兼容提示。
func TestReadNewLockNoWarnings(t *testing.T) {
	registry := lockTestRegistry()
	s := NewSolver(registry, PolicyMajor)
	res, err := s.Solve(lockTestRoots())
	if err != nil {
		t.Fatalf("求解失败: %v", err)
	}
	lock, err := LockFromResolution(res, registry, PolicyMajor, []string{"app"})
	if err != nil {
		t.Fatalf("生成锁定结果失败: %v", err)
	}
	path := t.TempDir() + "/depres-lock.json"
	if err := WriteLock(path, lock); err != nil {
		t.Fatalf("写入锁定文件失败: %v", err)
	}

	back, warnings, err := ReadLockCompat(path)
	if err != nil {
		t.Fatalf("读取新锁定文件失败: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("新锁定文件不应产生兼容提示: %v", warnings)
	}
	// 写后回读必须保持内容一致
	data1, _ := marshalLock(lock)
	data2, _ := marshalLock(back)
	if !bytes.Equal(data1, data2) {
		t.Fatalf("写后回读内容不一致:\n%s\n---\n%s", data1, data2)
	}
}
