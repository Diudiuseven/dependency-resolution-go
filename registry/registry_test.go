package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Diudiuseven/dependency-resolution-go/semver"
)

func TestMemoryRegistrySortedAndDedup(t *testing.T) {
	r, err := NewMemory([]Release{
		{Package: "lib", Version: semver.MustParse("2.0.0")},
		{Package: "lib", Version: semver.MustParse("1.0.0")},
		{Package: "lib", Version: semver.MustParse("1.5.0")},
	})
	if err != nil {
		t.Fatal(err)
	}
	vs, err := r.Versions(context.Background(), "lib")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 || vs[0].String() != "1.0.0" || vs[2].String() != "2.0.0" {
		t.Fatalf("versions not sorted: %v", vs)
	}
	if err := r.Add(Release{Package: "lib", Version: semver.MustParse("1.0.0")}); err == nil {
		t.Fatal("duplicate version should be rejected")
	}
	if _, err := r.Versions(context.Background(), "nope"); err == nil {
		t.Fatal("missing package should error")
	}
}

func TestLoadIndexValidates(t *testing.T) {
	good := []byte(`{"packages":{"lib":[{"version":"1.0.0","deps":[{"name":"u","range":"^1"}]}]}}`)
	r, err := LoadIndex(good)
	if err != nil {
		t.Fatal(err)
	}
	vs, _ := r.Versions(context.Background(), "lib")
	if len(vs) != 1 {
		t.Fatalf("got %v", vs)
	}
	for _, bad := range []string{
		`{"packages":{"lib":[{"version":"bad"}]}}`,
		`{"packages":{"lib":[{"version":"1.0.0","deps":[{"name":"u","range":">>"}]}]}}`,
		`{not json`,
	} {
		if _, err := LoadIndex([]byte(bad)); err == nil {
			t.Fatalf("expected error for %s", bad)
		}
	}
}

func TestDirRegistry(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "example.com", "lib")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.2.3.json"),
		[]byte(`{"hash":"sha256:x","deps":[{"name":"u","range":"^1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &DirRegistry{Root: root}
	vs, err := r.Versions(context.Background(), "example.com/lib")
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].String() != "1.2.3" {
		t.Fatalf("got %v", vs)
	}
	rel, err := r.Release(context.Background(), "example.com/lib", vs[0])
	if err != nil {
		t.Fatal(err)
	}
	if rel.Hash != "sha256:x" || len(rel.Deps) != 1 {
		t.Fatalf("release=%+v", rel)
	}
	if _, err := r.Versions(context.Background(), "../escape"); err == nil {
		t.Fatal("path traversal should be rejected")
	}
}
