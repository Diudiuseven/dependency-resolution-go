package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestCLISmoke builds the binary into a temp dir and exercises the whole
// local-only pipeline. It must succeed without any network access.
func TestCLISmoke(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "depresolve")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}

	index := filepath.Join(dir, "index.json")
	if err := os.WriteFile(index, []byte(`{
	  "packages": {
	    "example.com/lib": [
	      {"version":"1.0.0","hash":"sha256:one"},
	      {"version":"1.5.0","hash":"sha256:five","deps":[{"name":"example.com/util","range":"^1.0.0"}]},
	      {"version":"2.0.0","hash":"sha256:two"}
	    ],
	    "example.com/util": [
	      {"version":"1.0.0","hash":"sha256:util"}
	    ]
	  }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "app.json")
	if err := os.WriteFile(manifest, []byte(
		`{"package":"app","requires":{"example.com/lib":"^1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(dir, "dep.lock")
	run := func(args ...string) string {
		cmd := exec.Command(bin, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("command %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}
	out := run("resolve", "--manifest", manifest, "--index", index, "--out", lock)
	if !contains(out, "example.com/lib@1.5.0") {
		t.Fatalf("resolve output unexpected:\n%s", out)
	}
	data1, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	_ = run("resolve", "--manifest", manifest, "--index", index, "--out", lock)
	data2, err := os.ReadFile(lock)
	if err != nil {
		t.Fatal(err)
	}
	if string(data1) != string(data2) {
		t.Fatal("repeated resolve produced different lock bytes")
	}
	if out := run("verify", "--manifest", manifest, "--index", index, "--lock", lock); !contains(out, "up to date") {
		t.Fatalf("verify output: %s", out)
	}
	root := filepath.Join(dir, "modules")
	if out := run("install", "--index", index, "--lock", lock, "--root", root); !contains(out, "installation verified") {
		t.Fatalf("install output: %s", out)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
