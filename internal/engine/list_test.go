package engine

import (
	"path/filepath"
	"testing"

	"backup-cse/internal/conceal"
)

func newListTestDB(t *testing.T) *conceal.DB {
	t.Helper()
	db, err := conceal.Open(filepath.Join(t.TempDir(), "conceal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	files := map[string]conceal.FileRecord{
		"photo/2024/a.jpg":     {Concealed: "u1", Size: 100, MTimeNano: 1000},
		"photo/2024/b.jpg":     {Concealed: "u2", Size: 200, MTimeNano: 2000},
		"photo/2023/old.jpg":   {Concealed: "u3", Size: 50, MTimeNano: 3000},
		"photography/note.txt": {Concealed: "u4", Size: 10, MTimeNano: 4000},
		"top.txt":              {Concealed: "u5", Size: 5, MTimeNano: 5000},
	}
	for rel, rec := range files {
		if err := db.PutFile(rel, rec); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.EnsureDirs([]string{"photo/2024", "photo/2023", "photography", "photo/empty"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func paths(entries []ListEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

func TestCollectEntriesAll(t *testing.T) {
	db := newListTestDB(t)
	res, err := collectEntries(db, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 5 || res.Dirs != 0 {
		t.Fatalf("files=%d dirs=%d, want 5/0", res.Files, res.Dirs)
	}
	if res.TotalBytes != 365 {
		t.Errorf("total bytes = %d, want 365", res.TotalBytes)
	}
	// Path 昇順にソートされていること。
	got := paths(res.Entries)
	want := []string{
		"photo/2023/old.jpg",
		"photo/2024/a.jpg",
		"photo/2024/b.jpg",
		"photography/note.txt",
		"top.txt",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// ファイルエントリの Size / MTime が保持されていること。
	if res.Entries[0].Size != 50 || res.Entries[0].MTime.UnixNano() != 3000 {
		t.Errorf("entry[0] = %+v", res.Entries[0])
	}
}

func TestCollectEntriesSubpath(t *testing.T) {
	db := newListTestDB(t)
	res, err := collectEntries(db, "photo/2024", false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 2 || res.TotalBytes != 300 {
		t.Fatalf("files=%d total=%d, want 2/300", res.Files, res.TotalBytes)
	}
	// 境界: subpath "photo" は photography を拾わない。
	res, err = collectEntries(db, "photo", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range res.Entries {
		if e.Path == "photography/note.txt" {
			t.Errorf("subpath 'photo' incorrectly matched %q", e.Path)
		}
	}
	if res.Files != 3 { // photo/2024/a, photo/2024/b, photo/2023/old
		t.Errorf("files = %d, want 3", res.Files)
	}
}

func TestCollectEntriesIncludeDirs(t *testing.T) {
	db := newListTestDB(t)
	res, err := collectEntries(db, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 5 {
		t.Errorf("files = %d, want 5", res.Files)
	}
	// EnsureDirs は親ディレクトリも登録するため、少なくとも指定した4つは含む。
	if res.Dirs < 4 {
		t.Errorf("dirs = %d, want >= 4", res.Dirs)
	}
	seenEmpty := false
	for _, e := range res.Entries {
		if e.Path == "photo/empty" {
			if !e.IsDir {
				t.Error("photo/empty should be marked IsDir")
			}
			seenEmpty = true
		}
	}
	if !seenEmpty {
		t.Error("empty dir photo/empty not listed with --dirs")
	}
}
