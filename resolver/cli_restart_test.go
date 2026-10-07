package resolver_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFile 写入测试辅助文件。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildDepresBinary 在临时目录编译当前源码的 depres 二进制。
// 两次求解分别用不同的二进制调用，等价于进程重启。
func buildDepresBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "depres")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", binary, "github.com/Diudiuseven/dependency-resolution-go/cmd/depres")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("编译 depres 失败: %v\n%s", err, out)
	}
	return binary
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// 测试位于 <root>/resolver，向上一级即模块根目录。
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

// prepareCrashFixture 构造会触发“回溯残留未选定槽位”的文件 registry 与清单。
func prepareCrashFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	reg := filepath.Join(root, "registry")
	add := func(name, version, deps string) {
		var depMap any
		if deps != "" {
			if err := json.Unmarshal([]byte(deps), &depMap); err != nil {
				t.Fatal(err)
			}
		}
		m := map[string]any{"name": name, "version": version}
		if depMap != nil {
			m["dependencies"] = depMap
		}
		data, _ := json.MarshalIndent(m, "", "  ")
		writeFile(t, filepath.Join(reg, name, version+".json"), string(data))
	}
	add("x", "1.5.0", `{"d":"^1.0.0","e":"^1.0.0"}`)
	add("x", "1.0.0", `{"d":"^1.0.0"}`)
	add("d", "1.0.0", "")
	add("e", "1.0.0", `{"g":"^2.0.0"}`)
	add("g", "1.0.0", "")

	manifest := map[string]any{
		"name":         "app",
		"version":      "1.0.0",
		"dependencies": map[string]string{"x": "^1.0.0"},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	writeFile(t, filepath.Join(root, "app.json"), string(data))
	return root
}

// TestCLILockReproducibleAcrossProcessRestarts 连续两个独立进程求解同一输入，
// 写出的锁定结果必须逐字节一致，且不包含时间戳字段。
func TestCLILockReproducibleAcrossProcessRestarts(t *testing.T) {
	binary := buildDepresBinary(t)
	root := prepareCrashFixture(t)

	run := func(lockName string) []byte {
		lockPath := filepath.Join(root, lockName)
		cmd := exec.Command(binary, "resolve",
			"-registry", filepath.Join(root, "registry"),
			"-manifest", filepath.Join(root, "app.json"),
			"-lock", lockPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("depres resolve 失败: %v\n%s", err, out)
		}
		data, err := os.ReadFile(lockPath)
		if err != nil {
			t.Fatalf("读取锁定结果失败: %v", err)
		}
		return data
	}

	first := run("first.json")
	second := run("second.json")
	if !bytes.Equal(first, second) {
		t.Fatalf("进程重启后锁定结果不一致:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(string(first), "resolved_at") {
		t.Fatalf("锁定结果不得包含时间戳字段:\n%s", first)
	}
	// 未选定槽位必须如实固化。
	if !strings.Contains(string(first), "unresolved_slots") ||
		!strings.Contains(string(first), `"name": "e"`) {
		t.Fatalf("锁定结果必须包含未选定槽位 e:\n%s", first)
	}
}

// TestCLIExplainDoesNotCrash 对会触发回溯残留槽位的清单打开 -explain，
// 进程必须正常退出，stdout 含完整求解结论并如实呈现未选定版本的 e。
func TestCLIExplainDoesNotCrash(t *testing.T) {
	binary := buildDepresBinary(t)
	root := prepareCrashFixture(t)
	lockPath := filepath.Join(root, "lock.json")

	cmd := exec.Command(binary, "resolve",
		"-registry", filepath.Join(root, "registry"),
		"-manifest", filepath.Join(root, "app.json"),
		"-lock", lockPath,
		"-explain")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("-explain 模式进程崩溃: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"求解结果", "x (boundary=1): 1.0.0", "e", "未选定具体版本", "汇总"} {
		if !strings.Contains(out, want) {
			t.Fatalf("说明输出缺少 %q:\n%s", want, out)
		}
	}
}
