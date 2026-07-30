package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"backup-cse/internal/conceal"
	"backup-cse/internal/config"
	"backup-cse/internal/storage"
)

// ListEntry は conceal DB 上の1エントリ(バックアップ済みのオリジナルパス)。
type ListEntry struct {
	Path  string    // ソースルート相対・スラッシュ区切り
	IsDir bool      // 登録済みディレクトリなら true
	Size  int64     // ファイルの平文サイズ(ディレクトリでは 0)
	MTime time.Time // ファイルの更新日時(ディレクトリではゼロ値)
}

// ListResult は List の結果。
type ListResult struct {
	Entries    []ListEntry
	Files      int
	Dirs       int
	TotalBytes int64
}

// ListOptions は List の実行パラメータ。
type ListOptions struct {
	Config     *config.Config
	SourceName string
	// Subpath はソースルート相対(または source 配下の絶対パス)。空でなければ
	// そのパス配下のエントリだけを一覧する。
	Subpath string
	// IncludeDirs が true なら登録済みディレクトリも一覧に含める。
	IncludeDirs bool
	Logger      *slog.Logger
}

// List は S3 上の conceal DB を取得し、バックアップ済みのオリジナルパス一覧を返す。
// 読み取り専用で、S3 への書き込みは行わない。
func List(ctx context.Context, opts ListOptions) (ListResult, error) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	src, err := opts.Config.Source(opts.SourceName)
	if err != nil {
		return ListResult{}, err
	}
	subpath, err := normalizeSubpath(opts.Subpath, src.Path)
	if err != nil {
		return ListResult{}, err
	}
	key, err := opts.Config.LoadKey()
	if err != nil {
		return ListResult{}, err
	}
	bkt := opts.Config.Destination.Bucket
	client, err := storage.New(ctx, bkt.Region, bkt.Bucket, bkt.Prefix, key)
	if err != nil {
		return ListResult{}, err
	}

	logger.Info("listing conceal db",
		"source", opts.SourceName, "subpath", subpath, "include_dirs", opts.IncludeDirs)

	workdir, err := os.MkdirTemp("", "backup-cse-")
	if err != nil {
		return ListResult{}, err
	}
	defer os.RemoveAll(workdir)

	dbPath := filepath.Join(workdir, concealDBName)
	dbKey := opts.SourceName + "/metadata/" + concealDBName
	if err := client.DownloadToFile(ctx, dbKey, dbPath); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return ListResult{}, fmt.Errorf("no backup found for source %q (missing %s)", opts.SourceName, dbKey)
		}
		return ListResult{}, err
	}
	db, err := conceal.Open(dbPath)
	if err != nil {
		return ListResult{}, err
	}
	defer db.Close()

	res, err := collectEntries(db, subpath, opts.IncludeDirs)
	if err != nil {
		return ListResult{}, err
	}
	if subpath != "" && len(res.Entries) == 0 {
		logger.Warn("no entries found under subpath", "subpath", subpath)
	}
	return res, nil
}

// collectEntries は開いている conceal DB からエントリを収集・集計する。
// subpath が空でなければその配下だけに絞る。S3 に依存しないため単体テスト可能。
func collectEntries(db *conceal.DB, subpath string, includeDirs bool) (ListResult, error) {
	var res ListResult
	err := db.Files(func(rel string, rec conceal.FileRecord) error {
		if !matchSubpath(subpath, rel) {
			return nil
		}
		res.Entries = append(res.Entries, ListEntry{
			Path:  rel,
			Size:  rec.Size,
			MTime: time.Unix(0, rec.MTimeNano),
		})
		res.Files++
		res.TotalBytes += rec.Size
		return nil
	})
	if err != nil {
		return ListResult{}, err
	}
	if includeDirs {
		err = db.Dirs(func(rel string) error {
			if !matchSubpath(subpath, rel) {
				return nil
			}
			res.Entries = append(res.Entries, ListEntry{Path: rel, IsDir: true})
			res.Dirs++
			return nil
		})
		if err != nil {
			return ListResult{}, err
		}
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		return res.Entries[i].Path < res.Entries[j].Path
	})
	return res, nil
}
