// Package engine はバックアップ・リストアの本体処理を実装する。
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"

	"backup-cse/internal/conceal"
	"backup-cse/internal/config"
	"backup-cse/internal/console"
	"backup-cse/internal/storage"
)

const (
	concealDBName   = "conceal.db"
	finalizeTimeout = 5 * time.Minute
)

// BackupOptions は Backup の実行パラメータ。
type BackupOptions struct {
	Config     *config.Config
	SourceName string
	DryRun     bool
	Parallel   int
	Logger     *slog.Logger
	// Subpath はソースルート相対(または source 配下の絶対パス)。空でなければ
	// そのパス配下のファイルだけをバックアップ対象にする。
	Subpath string
	// LogPath はローカル NDJSON ログのパス。空でなければバックアップ後に
	// 暗号化して <source>/metadata/logs/ へアップロードする。
	LogPath  string
	Progress *console.Tracker // nil 可
}

// BackupSummary はバックアップ結果の集計。
type BackupSummary struct {
	Scanned       int   // スキャンしたローカルファイル数
	Candidates    int   // アップロード対象と判定された数
	Uploaded      int   // 実際にアップロードした数
	Skipped       int   // 変更なしでスキップした数
	Failed        int   // 失敗した数
	UploadedBytes int64 // アップロードした平文バイト数

	API storage.BillableSummary // コストに効く S3 API 呼び出しの集計
}

// Backup は1ソースをS3へ差分バックアップする。
// シグナル等で ctx がキャンセルされた場合も、conceal DB とログのアップロード
// (finalize) は行い、S3 と DB の整合性を保つ。
func Backup(ctx context.Context, opts BackupOptions) (BackupSummary, error) {
	var sum BackupSummary
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
	excludes, err := src.CompiledExcludes()
	if err != nil {
		return sum, err
	}
	subpath, err := normalizeSubpath(opts.Subpath, src.Path)
	if err != nil {
		return sum, err
	}
	class, err := storage.ParseStorageClass(src.StorageClass)
	if err != nil {
		return sum, fmt.Errorf("source %s: %w", opts.SourceName, err)
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

	logger.Info("backup started",
		"source", opts.SourceName, "path", src.Path, "subpath", subpath,
		"bucket", bkt.Bucket, "prefix", bkt.Prefix,
		"storage_class", string(class), "parallel", parallel, "dry_run", opts.DryRun)

	workdir, err := os.MkdirTemp("", "backup-cse-")
	if err != nil {
		return sum, err
	}
	defer os.RemoveAll(workdir)

	// 既存の conceal DB を S3 から取得(初回は存在しない)。
	dbPath := filepath.Join(workdir, concealDBName)
	dbKey := opts.SourceName + "/metadata/" + concealDBName
	switch err := client.DownloadToFile(ctx, dbKey, dbPath); {
	case errors.Is(err, storage.ErrNotFound):
		logger.Info("no existing conceal db; starting fresh")
	case err != nil:
		return sum, err
	}
	db, err := conceal.Open(dbPath)
	if err != nil {
		return sum, err
	}
	dbClosed := false
	closeDB := func() {
		if !dbClosed {
			dbClosed = true
			if cerr := db.Close(); cerr != nil {
				logger.Error("failed to close conceal db", "error", cerr.Error())
			}
		}
	}
	defer closeDB()

	// S3 上の実在オブジェクトを 1000 件ずつリストする。
	dataPrefix := opts.SourceName + "/data/"
	remote, err := client.ListKeys(ctx, dataPrefix)
	if err != nil {
		return sum, err
	}
	logger.Info("remote objects listed", "count", len(remote))

	scanRoot := src.Path
	if subpath != "" {
		scanRoot = filepath.Join(src.Path, filepath.FromSlash(subpath))
	}
	files, dirs, err := walkSource(src.Path, scanRoot, excludes, logger)
	if err != nil {
		if subpath != "" {
			return sum, fmt.Errorf("scan subpath %q under %s: %w", subpath, src.Path, err)
		}
		return sum, fmt.Errorf("scan %s: %w", src.Path, err)
	}
	sum.Scanned = len(files)
	logger.Info("local scan finished", "files", len(files), "dirs", len(dirs))

	if !opts.DryRun {
		if err := db.EnsureDirs(dirs); err != nil {
			return sum, err
		}
	}

	// 差分計算。
	var items []backupItem
	var newRels []string
	for _, f := range files {
		rec, ok, err := db.File(f.rel)
		if err != nil {
			return sum, err
		}
		need, reason := needsUpload(rec, ok, f, remote)
		if !need {
			sum.Skipped++
			continue
		}
		it := backupItem{file: f, reason: reason}
		if ok {
			it.concealed = rec.Concealed
		} else {
			newRels = append(newRels, f.rel)
		}
		items = append(items, it)
	}
	sum.Candidates = len(items)

	if opts.DryRun {
		var bytes int64
		for _, it := range items {
			logger.Info("would upload", "path", it.file.rel, "size", it.file.size, "reason", it.reason)
			bytes += it.file.size
		}
		closeDB()
		logger.Info("backup finished (dry-run)",
			"scanned", sum.Scanned, "would_upload", sum.Candidates,
			"would_upload_bytes", bytes, "skipped", sum.Skipped)
		sum.API = client.APIStats()
		logAPIUsage(logger, sum.API)
		return sum, nil
	}

	// 新規ファイルの秘匿化パスをまとめて採番する。
	if len(newRels) > 0 {
		concealedMap, err := db.ConcealFilePaths(newRels)
		if err != nil {
			return sum, err
		}
		for i := range items {
			if items[i].concealed == "" {
				items[i].concealed = concealedMap[items[i].file.rel]
			}
		}
	}

	var totalBytes int64
	for _, it := range items {
		totalBytes += it.file.size
	}
	opts.Progress.Start("backup", len(items), totalBytes)

	var uploaded, failed, uploadedBytes atomic.Int64
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
			err := uploadOne(gctx, client, db, src.Path, dataPrefix, it, class, logger)
			if err != nil {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				// 個別ファイルの失敗では全体を止めない。
				failed.Add(1)
				opts.Progress.FileFailed()
				logger.Error("upload failed", "path", it.file.rel, "error", err.Error())
				return nil
			}
			uploaded.Add(1)
			uploadedBytes.Add(it.file.size)
			opts.Progress.FileDone(it.file.size)
			return nil
		})
	}
	runErr := g.Wait()
	opts.Progress.Stop()

	sum.Uploaded = int(uploaded.Load())
	sum.Failed = int(failed.Load())
	sum.UploadedBytes = uploadedBytes.Load()

	if runErr != nil {
		logger.Warn("backup interrupted; uploading metadata before exit (press Ctrl-C again to force quit)")
	}

	// finalize: 中断・一部失敗にかかわらず conceal DB とログをアップロードし、
	// S3 上のオブジェクトと DB の不整合を防ぐ。
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalizeTimeout)
	defer cancel()

	closeDB()
	if err := uploadFile(fctx, client, dbKey, dbPath); err != nil {
		return sum, fmt.Errorf("upload conceal db: %w", err)
	}
	logger.Info("conceal db uploaded", "key", dbKey)

	logger.Info("backup finished",
		"scanned", sum.Scanned, "uploaded", sum.Uploaded,
		"uploaded_bytes", sum.UploadedBytes, "skipped", sum.Skipped,
		"failed", sum.Failed, "interrupted", runErr != nil)

	// この時点までの API 呼び出しを集計する(この直後のログアップロード
	// 1 回の PutObject は、集計値を含んだログを S3 へ上げる都合上含まれない)。
	sum.API = client.APIStats()
	logAPIUsage(logger, sum.API)

	if opts.LogPath != "" {
		logKey := opts.SourceName + "/metadata/logs/" +
			time.Now().UTC().Format("20060102T150405Z") + "-backup.ndjson"
		if err := uploadFile(fctx, client, logKey, opts.LogPath); err != nil {
			return sum, fmt.Errorf("upload log: %w", err)
		}
	}

	if runErr != nil {
		return sum, runErr
	}
	if sum.Failed > 0 {
		return sum, fmt.Errorf("%d file(s) failed to upload", sum.Failed)
	}
	return sum, nil
}

func uploadOne(ctx context.Context, client *storage.Client, db *conceal.DB, root, dataPrefix string, it backupItem, class types.StorageClass, logger *slog.Logger) error {
	path := filepath.Join(root, filepath.FromSlash(it.file.rel))
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	meta := map[string]string{
		storage.MetaMTime: strconv.FormatInt(it.file.mtimeNano, 10),
		storage.MetaSize:  strconv.FormatInt(it.file.size, 10),
	}
	if err := client.Upload(ctx, dataPrefix+it.concealed, f, it.file.size, class, meta); err != nil {
		return err
	}
	// DB への記録はアップロード成功後に行う。逆順だと、アップロード失敗時に
	// DB だけが更新され「存在しないオブジェクトを指すレコード」ができてしまう。
	if err := db.PutFile(it.file.rel, conceal.FileRecord{
		Concealed: it.concealed,
		Size:      it.file.size,
		MTimeNano: it.file.mtimeNano,
	}); err != nil {
		return err
	}
	logger.Info("uploaded", "path", it.file.rel, "size", it.file.size, "reason", it.reason)
	return nil
}

// uploadFile はローカルファイルを暗号化して STANDARD クラスでアップロードする。
// metadata 系オブジェクト(conceal DB・ログ)用。
func uploadFile(ctx context.Context, client *storage.Client, key, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// conceal DB は多数のファイルを持つと大きくなり得るため、サイズをパート
	// サイズ算出に渡す。取得できなければ既定(小さいパート)にフォールバック。
	var size int64
	if info, serr := f.Stat(); serr == nil {
		size = info.Size()
	}
	return client.Upload(ctx, key, f, size, types.StorageClassStandard, nil)
}
