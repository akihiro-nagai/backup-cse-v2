package engine

import (
	"path/filepath"
	"testing"
)

func TestNormalizeSubpath(t *testing.T) {
	root := t.TempDir()

	t.Run("valid", func(t *testing.T) {
		cases := map[string]string{
			"":            "",
			"   ":         "",
			".":           "",
			"2024/photos": "2024/photos",
			"./a/b":       "a/b",
			"a/b/":        "a/b",
			"a//b":        "a/b",
			"dir":         "dir",
			filepath.FromSlash("nested/deeper/file.txt"): "nested/deeper/file.txt",
		}
		for raw, want := range cases {
			got, err := normalizeSubpath(raw, root)
			if err != nil {
				t.Errorf("normalizeSubpath(%q): unexpected error: %v", raw, err)
				continue
			}
			if got != want {
				t.Errorf("normalizeSubpath(%q) = %q, want %q", raw, got, want)
			}
		}
	})

	t.Run("absolute inside source", func(t *testing.T) {
		abs := filepath.Join(root, "2024", "photos")
		got, err := normalizeSubpath(abs, root)
		if err != nil {
			t.Fatal(err)
		}
		if got != "2024/photos" {
			t.Errorf("got %q, want 2024/photos", got)
		}
	})

	t.Run("absolute equal to source root", func(t *testing.T) {
		got, err := normalizeSubpath(root, root)
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("rejects escapes", func(t *testing.T) {
		outside := t.TempDir() // root とは別のディレクトリ(兄弟)
		bad := []string{
			"..",
			"../x",
			"a/../../b",
			outside, // ソース外の絶対パス
			filepath.Join(root, "..", "sibling"),
		}
		for _, raw := range bad {
			if got, err := normalizeSubpath(raw, root); err == nil {
				t.Errorf("normalizeSubpath(%q) = %q, want error", raw, got)
			}
		}
	})
}

func TestMatchSubpath(t *testing.T) {
	cases := []struct {
		sub, rel string
		want     bool
	}{
		{"", "anything/at/all", true},
		{"", "", true},
		{"a/b", "a/b", true},            // 完全一致(単一ファイル)
		{"a/b", "a/b/c.txt", true},      // 配下
		{"a/b", "a/b/c/d.txt", true},    // さらに深い配下
		{"a/b", "a/bc.txt", false},      // 境界: a/b の兄弟
		{"a/b", "a", false},             // 親
		{"a/b", "x/a/b", false},         // 別の場所
		{"photo", "photography", false}, // 境界: prefix だが別ディレクトリ
		{"photo", "photo/2024.jpg", true},
	}
	for _, c := range cases {
		if got := matchSubpath(c.sub, c.rel); got != c.want {
			t.Errorf("matchSubpath(%q, %q) = %v, want %v", c.sub, c.rel, got, c.want)
		}
	}
}
