package manifest

import (
	"context"
	"testing"
)

func TestFromFileSortsDepsAndRecordsSource(t *testing.T) {
	data := []byte(`{"package":"app","requires":{"zlib":"^1.0","alib":"~2.0"}}`)
	m, err := FromFile(data, "app.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Deps) != 2 || m.Deps[0].Name != "alib" || m.Deps[1].Name != "zlib" {
		t.Fatalf("deps not sorted: %+v", m.Deps)
	}
	if m.Deps[0].Src.Package_ != "app" || m.Deps[0].Src.Range != "~2.0" {
		t.Fatalf("source not recorded: %+v", m.Deps[0].Src)
	}
}

func TestFromFileRejectsMissingPackage(t *testing.T) {
	if _, err := FromFile([]byte(`{"requires":{}}`), "x.json"); err == nil {
		t.Fatal("expected error for missing package name")
	}
	if _, err := FromFile([]byte(`{bad`), "x.json"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestStaticReaderDeterministic(t *testing.T) {
	r := NewStaticReader([]Manifest{
		{Package: "b"}, {Package: "a"}, {Package: "c"},
	})
	if got := r.Packages(); got[0] != "a" || got[2] != "c" {
		t.Fatalf("packages not sorted: %v", got)
	}
	if _, err := r.ReadManifest(context.Background(), "zzz"); err == nil {
		t.Fatal("expected not found")
	}
}
