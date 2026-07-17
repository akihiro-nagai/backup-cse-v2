package conceal

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "conceal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestConcealFilePaths(t *testing.T) {
	db := openTestDB(t)
	m, err := db.ConcealFilePaths([]string{
		"a/b/one.txt",
		"a/b/two.txt",
		"a/three.txt",
		"root.txt",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 全成分が UUID v4 であること。
	for rel, concealed := range m {
		parts := strings.Split(concealed, "/")
		if len(parts) != len(strings.Split(rel, "/")) {
			t.Errorf("%s: depth mismatch: %s", rel, concealed)
		}
		for _, p := range parts {
			if !uuidRe.MatchString(p) {
				t.Errorf("%s: component %q is not a uuid v4", rel, p)
			}
		}
	}

	// 同じディレクトリは同じ UUID に写像されること。
	dirOf := func(s string) string { return s[:strings.LastIndex(s, "/")] }
	if dirOf(m["a/b/one.txt"]) != dirOf(m["a/b/two.txt"]) {
		t.Error("same dir a/b mapped to different uuids")
	}
	if !strings.HasPrefix(m["a/b/one.txt"], strings.Split(m["a/three.txt"], "/")[0]+"/") {
		t.Error("dir a mapped inconsistently between a/b/one.txt and a/three.txt")
	}

	// ファイル成分はそれぞれ別 UUID であること。
	if m["a/b/one.txt"] == m["a/b/two.txt"] {
		t.Error("distinct files mapped to the same concealed path")
	}

	// 別呼び出しでもディレクトリ UUID は安定していること。
	m2, err := db.ConcealFilePaths([]string{"a/b/later.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if dirOf(m2["a/b/later.txt"]) != dirOf(m["a/b/one.txt"]) {
		t.Error("dir uuid not stable across calls")
	}
}

func TestPutFileAndIterate(t *testing.T) {
	db := openTestDB(t)
	rec := FileRecord{Concealed: "u1/u2", Size: 42, MTimeNano: 1234567890}
	if err := db.PutFile("dir/file.txt", rec); err != nil {
		t.Fatal(err)
	}

	got, ok, err := db.File("dir/file.txt")
	if err != nil || !ok {
		t.Fatalf("File: ok=%v err=%v", ok, err)
	}
	if got != rec {
		t.Fatalf("got %+v, want %+v", got, rec)
	}

	if _, ok, _ := db.File("missing"); ok {
		t.Fatal("missing file reported as existing")
	}

	count := 0
	err = db.Files(func(rel string, r FileRecord) error {
		count++
		if rel != "dir/file.txt" || r != rec {
			t.Errorf("unexpected entry %s %+v", rel, r)
		}
		return nil
	})
	if err != nil || count != 1 {
		t.Fatalf("Files: count=%d err=%v", count, err)
	}
}

func TestEnsureDirs(t *testing.T) {
	db := openTestDB(t)
	if err := db.EnsureDirs([]string{"a", "a/b/c", "empty"}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	if err := db.Dirs(func(rel string) error {
		seen[rel] = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"a", "a/b", "a/b/c", "empty"} {
		if !seen[want] {
			t.Errorf("dir %q not registered", want)
		}
	}
}

func TestReopenPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conceal.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := FileRecord{Concealed: "x", Size: 1, MTimeNano: 2}
	if err := db.PutFile("f", rec); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	got, ok, err := db2.File("f")
	if err != nil || !ok || got != rec {
		t.Fatalf("record not persisted: ok=%v err=%v got=%+v", ok, err, got)
	}
}
