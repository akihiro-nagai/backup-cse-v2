package engine

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"regexp"
)

// localFile はローカルでスキャンした1ファイル。
type localFile struct {
	rel       string // ソースルートからの相対パス(スラッシュ区切り)
	size      int64
	mtimeNano int64
}

// walkSource は root 以下を走査し、除外パターンに一致しない
// 通常ファイルとディレクトリを収集する。
// 除外パターンはスラッシュ区切りのフルパスに対して照合する。
// ディレクトリは末尾に "/" を付けて照合するため、".*/node_modules/.*" の
// ようなパターンでディレクトリごと枝刈りされる。
func walkSource(root string, excludes []*regexp.Regexp, logger *slog.Logger) (files []localFile, dirs []string, err error) {
	root = filepath.Clean(root)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if path == root {
				return werr
			}
			logger.Warn("skipping unreadable path", "path", path, "error", werr.Error())
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		full := filepath.ToSlash(path)
		if d.IsDir() {
			if excluded(excludes, full+"/") {
				return fs.SkipDir
			}
			dirs = append(dirs, rel)
			return nil
		}
		if excluded(excludes, full) {
			return nil
		}
		if !d.Type().IsRegular() {
			logger.Warn("skipping non-regular file", "path", path)
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			logger.Warn("skipping unstatable file", "path", path, "error", ierr.Error())
			return nil
		}
		files = append(files, localFile{
			rel:       rel,
			size:      info.Size(),
			mtimeNano: info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return files, dirs, nil
}

func excluded(pats []*regexp.Regexp, s string) bool {
	for _, re := range pats {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}
