package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"backup-cse/internal/conceal"
	"backup-cse/internal/config"
	"backup-cse/internal/console"
	"backup-cse/internal/storage"
)

// RestoreOptions は Restore の実行パラメータ。
type RestoreOptions struct {
	Config     *config.Config
	SourceName string
	// DestDir はリストア先。空ならソースの path に復元する。
	DestDir  string
	DryRun   bool
	Parallel int
	Logger   *slog.Logger
	Progress *console.Tracker // nil 可
}

// RestoreSummary はリストア結果の集計。
type RestoreSummary struct {
	Total         int   // conceal DB 上の全ファイル数
	Candidates    int   // リストア対象と判定された数
	Restored      int   // 実際にリストアした数
	Skipped       int   // ローカルが最新でスキップした数
	Failed        int   // 失敗した数(NotRestored を含む)
	NotRestored   int   // Glacier にアーカイブ済みで復元リクエスト未実施のため失敗した数
	RestoredBytes int64 // リストアした平文バイト数

	API storage.BillableSummary // コストに効く S3 API 呼び出しの集計
}

type restoreItem struct {
	rel string
	rec conceal.FileRecord
}

// Restore はS3上のバックアップから差分リストアする。
// ローカルのサイズ・更新日時が conceal DB の記録と一致するファイルはスキップする。
func Restore(ctx context.Context, opts RestoreOptions) (RestoreSummary, error) {
	var sum RestoreSummary
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	parallel := opts.Parallel
	if parallel < 1 {
		parallel = 1
	}

	src, err := opts.Config.Source(opts.SourceName)
	if err != nil {
		return sum, err
	}
	dest := opts.DestDir
	if dest == "" {
		dest = src.Path
	}
	key, err := opts.Config.LoadKey()
	if err != nil {
		return sum, err
	}
	bkt := opts.Config.Destination.Bucket
	client, err := storage.New(ctx, bkt.Region, bkt.Bucket, bkt.Prefix, key)
	if err != nil {
		return sum, err
	}

	logger.Info("restore started",
		"source", opts.SourceName, "dest", dest,
		"bucket", bkt.Bucket, "prefix", bkt.Prefix,
		"parallel", parallel, "dry_run", opts.DryRun)

	workdir, err := os.MkdirTemp("", "backup-cse-")
	if err != nil {
		return sum, err
	}
	defer os.RemoveAll(workdir)

	dbPath := filepath.Join(workdir, concealDBName)
	dbKey := opts.SourceName + "/metadata/" + concealDBName
	if err := client.DownloadToFile(ctx, dbKey, dbPath); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return sum, fmt.Errorf("no backup found for source %q (missing %s)", opts.SourceName, dbKey)
		}
		return sum, err
	}
	db, err := conceal.Open(dbPath)
	if err != nil {
		return sum, err
	}
	defer db.Close()

	dataPrefix := opts.SourceName + "/data/"
	remote, err := client.ListKeys(ctx, dataPrefix)
	if err != nil {
		return sum, err
	}
	logger.Info("remote objects listed", "count", len(remote))

	// ディレクトリ構造(空ディレクトリを含む)を復元する。
	if !opts.DryRun {
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return sum, err
		}
		err = db.Dirs(func(rel string) error {
			return os.MkdirAll(filepath.Join(dest, filepath.FromSlash(rel)), 0o755)
		})
		if err != nil {
			return sum, err
		}
	}

	// 差分計算。
	var items []restoreItem
	err = db.Files(func(rel string, rec conceal.FileRecord) error {
		sum.Total++
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if info, serr := os.Stat(target); serr == nil &&
			info.Size() == rec.Size && info.ModTime().UnixNano() == rec.MTimeNano {
			sum.Skipped++
			return nil
		}
		if _, ok := remote[rec.Concealed]; !ok {
			sum.Failed++
			logger.Error("object missing on S3; cannot restore", "path", rel, "key", dataPrefix+rec.Concealed)
			return nil
		}
		items = append(items, restoreItem{rel: rel, rec: rec})
		return nil
	})
	if err != nil {
		return sum, err
	}
	sum.Candidates = len(items)

	if opts.DryRun {
		var bytes int64
		for _, it := range items {
			logger.Info("would restore", "path", it.rel, "size", it.rec.Size)
			bytes += it.rec.Size
		}
		logger.Info("restore finished (dry-run)",
			"total", sum.Total, "would_restore", sum.Candidates,
			"would_restore_bytes", bytes, "skipped", sum.Skipped, "failed", sum.Failed)
		sum.API = client.APIStats()
		logAPIUsage(logger, sum.API)
		if sum.Failed > 0 {
			return sum, fmt.Errorf("%d file(s) are missing on S3", sum.Failed)
		}
		return sum, nil
	}

	var totalBytes int64
	for _, it := range items {
		totalBytes += it.rec.Size
	}
	opts.Progress.Start("restore", len(items), totalBytes)

	var restored, failed, notRestored, restoredBytes atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for _, it := range items {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			err := restoreOne(gctx, client, dest, dataPrefix, it)
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				failed.Add(1)
				opts.Progress.FileFailed()
				if errors.Is(err, storage.ErrNotRestored) {
					// Glacier にアーカイブされたまま復元リクエストされていない。
					notRestored.Add(1)
					logger.Error("restore failed: object is archived and not restored; run 'backup-cse restore-request' first",
						"path", it.rel, "key", dataPrefix+it.rec.Concealed)
				} else {
					logger.Error("restore failed", "path", it.rel, "error", err.Error())
				}
				return nil
			}
			restored.Add(1)
			restoredBytes.Add(it.rec.Size)
			opts.Progress.FileDone(it.rec.Size)
			logger.Info("restored", "path", it.rel, "size", it.rec.Size)
			return nil
		})
	}
	runErr := g.Wait()
	opts.Progress.Stop()

	sum.Restored = int(restored.Load())
	sum.NotRestored = int(notRestored.Load())
	sum.Failed += int(failed.Load())
	sum.RestoredBytes = restoredBytes.Load()

	logger.Info("restore finished",
		"total", sum.Total, "restored", sum.Restored,
		"restored_bytes", sum.RestoredBytes, "skipped", sum.Skipped,
		"failed", sum.Failed, "not_restored", sum.NotRestored, "interrupted", runErr != nil)

	sum.API = client.APIStats()
	logAPIUsage(logger, sum.API)

	if sum.NotRestored > 0 {
		logger.Warn("some objects are archived in S3 Glacier and were not restored; request restoration then re-run restore",
			"not_restored", sum.NotRestored,
			"hint", fmt.Sprintf("backup-cse restore-request <config.yaml> %s", opts.SourceName))
	}

	if runErr != nil {
		return sum, runErr
	}
	if sum.Failed > 0 {
		if sum.NotRestored > 0 {
			return sum, fmt.Errorf("%d file(s) failed to restore; %d are archived and not restored (run 'backup-cse restore-request <config.yaml> %s' first)",
				sum.Failed, sum.NotRestored, opts.SourceName)
		}
		return sum, fmt.Errorf("%d file(s) failed to restore", sum.Failed)
	}
	return sum, nil
}

func restoreOne(ctx context.Context, client *storage.Client, dest, dataPrefix string, it restoreItem) error {
	target := filepath.Join(dest, filepath.FromSlash(it.rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	// 一時ファイルへ復号してから rename することで、中断時に
	// 壊れた内容が本来のパスに残らないようにする。
	tmp := target + ".backup-cse-tmp"
	if err := client.DownloadToFile(ctx, dataPrefix+it.rec.Concealed, tmp); err != nil {
		return err
	}
	mtime := time.Unix(0, it.rec.MTimeNano)
	if err := os.Chtimes(tmp, mtime, mtime); err != nil {
		os.Remove(tmp)
		return err
	}
	// Windows では rename が既存ファイルを上書きできないため先に削除する。
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
