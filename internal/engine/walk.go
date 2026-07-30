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

// walkSource は scanRoot 以下を走査し、除外パターンに一致しない
// 通常ファイルとディレクトリを収集する。相対パス(rel)は relBase 基準で
// 算出するため、scanRoot が relBase のサブディレクトリでも DB キーは
// ソースルート相対のまま一貫する。scanRoot == relBase ならソース全体を走査する。
// scanRoot が単一ファイルの場合はそのファイルだけを対象にする。
// 除外パターンはスラッシュ区切りのフルパスに対して照合する。
// ディレクトリは末尾に "/" を付けて照合するため、".*/node_modules/.*" の
// ようなパターンでディレクトリごと枝刈りされる。
func walkSource(relBase, scanRoot string, excludes []*regexp.Regexp, logger *slog.Logger) (files []localFile, dirs []string, err error) {
	relBase = filepath.Clean(relBase)
	scanRoot = filepath.Clean(scanRoot)
	err = filepath.WalkDir(scanRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			if path == scanRoot {
				return werr
			}
			logger.Warn("skipping unreadable path", "path", path, "error", werr.Error())
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(relBase, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			// relBase 自身(ソースルート)は登録しない。
			return nil
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
