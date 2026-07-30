package engine

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// normalizeSubpath はユーザー指定のサブパスを、ソースルート相対・スラッシュ
// 区切りの形式(conceal DB や walk の rel キーと同じ形式)に正規化する。
//
//   - raw が空なら "" を返す(=ソース全体を対象とする現行動作)
//   - 絶対パスは sourceRoot 配下であることを要求し、相対化して返す
//   - 相対パスは sourceRoot 基準として解釈する
//   - いずれの場合も、結果が sourceRoot の外(先頭が ".." )になる指定はエラー
func normalizeSubpath(raw, sourceRoot string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	var rel string
	if filepath.IsAbs(raw) {
		root, err := filepath.Abs(sourceRoot)
		if err != nil {
			return "", err
		}
		r, err := filepath.Rel(root, filepath.Clean(raw))
		if err != nil {
			return "", fmt.Errorf("subpath %q is not inside source %q: %w", raw, sourceRoot, err)
		}
		rel = r
	} else {
		rel = filepath.Clean(raw)
	}

	rel = filepath.ToSlash(rel)
	rel = path.Clean(rel)

	if rel == "." || rel == "/" {
		return "", nil
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("subpath %q escapes the source root", raw)
	}
	return rel, nil
}

// matchSubpath は rel(ソースルート相対・スラッシュ区切り)が sub の配下、
// もしくは sub 自身(単一ファイル指定)かどうかを判定する。
// sub が空ならすべてにマッチする。
func matchSubpath(sub, rel string) bool {
	if sub == "" {
		return true
	}
	return rel == sub || strings.HasPrefix(rel, sub+"/")
}
