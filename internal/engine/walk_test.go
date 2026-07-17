package engine

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"testing"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalkSource(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "sub", "b.txt"), "b")
	writeFile(t, filepath.Join(root, "proj", "node_modules", "dep", "index.js"), "x")
	writeFile(t, filepath.Join(root, "proj", "main.go"), "y")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, "link.txt")); err != nil {
			t.Fatal(err)
		}
	}

	excludes := []*regexp.Regexp{regexp.MustCompile(`.*/node_modules/.*`)}
	files, dirs, err := walkSource(root, excludes, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	var rels []string
	for _, f := range files {
		rels = append(rels, f.rel)
	}
	slices.Sort(rels)
	want := []string{"a.txt", "proj/main.go", "sub/b.txt"}
	if !slices.Equal(rels, want) {
		t.Errorf("files = %v, want %v", rels, want)
	}

	slices.Sort(dirs)
	// node_modules ディレクトリは枝刈りされ dirs にも現れない。
	for _, d := range dirs {
		if regexp.MustCompile(`node_modules`).MatchString(d) {
			t.Errorf("excluded dir %q returned", d)
		}
	}
	if !slices.Contains(dirs, "empty") {
		t.Errorf("empty dir not collected: %v", dirs)
	}

	// サイズと mtime が入っていること。
	for _, f := range files {
		if f.size <= 0 || f.mtimeNano == 0 {
			t.Errorf("%s: size=%d mtime=%d", f.rel, f.size, f.mtimeNano)
		}
	}
}

func TestWalkSourceRootExclude(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "node_modules", "x.js"), "x")
	writeFile(t, filepath.Join(root, "keep.txt"), "k")

	excludes := []*regexp.Regexp{regexp.MustCompile(`.*/node_modules/.*`)}
	files, _, err := walkSource(root, excludes, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].rel != "keep.txt" {
		t.Errorf("files = %+v", files)
	}
}
