package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"golang.org/x/sync/errgroup"

	"backup-cse/internal/conceal"
	"backup-cse/internal/config"
	"backup-cse/internal/storage"
)

const defaultRestoreRequestDays = 7

// RestoreRequestOptions は RestoreRequest の実行パラメータ。
type RestoreRequestOptions struct {
	Config     *config.Config
	SourceName string
	// Tier は "standard"(既定) / "bulk" / "expedited"。Deep Archive では
	// expedited は使えない。
	Tier string
	// Days は復元した一時コピーを保持する日数。0 以下なら既定値(7日)。
	Days int32
	// Subpath はソースルート相対(または source 配下の絶対パス)。空でなければ
	// そのパス配下のファイルだけを復元リクエスト対象にする。
	Subpath  string
	DryRun   bool
	Parallel int
	Logger   *slog.Logger
}

// RestoreRequestSummary は RestoreRequest の結果集計。
type RestoreRequestSummary struct {
	Total      int // conceal DB 上の全ファイル数
	NotNeeded  int // Glacier 系ストレージクラスではなく復元不要
	Available  int // 既に復元済みで取得可能
	InProgress int // 復元リクエスト済みで完了待ち
	Requested  int // 今回新たに復元リクエストした数(dry-run では「予定」件数)
	Failed     int

	API storage.BillableSummary // コストに効く S3 API 呼び出しの集計
}

// RestoreRequest はソース内の各ファイルについて S3 Glacier の復元リクエスト
// (restore request)状況を確認し、未着手のものにリクエストを送る。
// リクエスト自体はすぐ返るが、実際にダウンロード可能になるまでは
// tier に応じて数時間〜48時間程度かかる。完了後は通常の restore を実行する。
func RestoreRequest(ctx context.Context, opts RestoreRequestOptions) (RestoreRequestSummary, error) {
	var sum RestoreRequestSummary
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	parallel := opts.Parallel
	if parallel < 1 {
		parallel = 1
	}
	tier, err := storage.ParseTier(opts.Tier)
	if err != nil {
		return sum, err
	}
	days := opts.Days
	if days < 1 {
		days = defaultRestoreRequestDays
	}

	src, err := opts.Config.Source(opts.SourceName)
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
	if class == types.StorageClassDeepArchive && tier == types.TierExpedited {
		return sum, errors.New("expedited tier is not supported for deep-archive; use standard or bulk")
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

	logger.Info("restore-request started",
		"source", opts.SourceName, "subpath", subpath,
		"tier", string(tier), "days", days, "dry_run", opts.DryRun)

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
	var concealedKeys []string
	err = db.Files(func(rel string, rec conceal.FileRecord) error {
		if !matchSubpath(subpath, rel) {
			return nil
		}
		concealedKeys = append(concealedKeys, rec.Concealed)
		return nil
	})
	if err != nil {
		return sum, err
	}
	sum.Total = len(concealedKeys)

	if subpath != "" && sum.Total == 0 {
		logger.Warn("no files found under subpath; nothing to request", "subpath", subpath)
	}

	var notNeeded, available, inProgress, requested, failed atomic.Int64
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for _, concealed := range concealedKeys {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			fullKey := dataPrefix + concealed
			status, err := client.Stat(gctx, fullKey)
			if err != nil {
				failed.Add(1)
				logger.Error("stat failed", "key", fullKey, "error", err.Error())
				return nil
			}
			switch status.Restore {
			case storage.RestoreNotNeeded:
				notNeeded.Add(1)
			case storage.RestoreAvailable:
				available.Add(1)
				logger.Info("already available", "key", fullKey)
			case storage.RestoreInProgress:
				inProgress.Add(1)
				logger.Info("restore already in progress", "key", fullKey)
			case storage.RestoreNotRequested:
				if opts.DryRun {
					requested.Add(1)
					logger.Info("would request restore", "key", fullKey, "storage_class", string(status.StorageClass))
					return nil
				}
				if err := client.RequestRestore(gctx, fullKey, tier, days); err != nil {
					failed.Add(1)
					logger.Error("restore request failed", "key", fullKey, "error", err.Error())
					return nil
				}
				requested.Add(1)
				logger.Info("restore requested", "key", fullKey, "storage_class", string(status.StorageClass), "tier", string(tier), "days", days)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return sum, err
	}

	sum.NotNeeded = int(notNeeded.Load())
	sum.Available = int(available.Load())
	sum.InProgress = int(inProgress.Load())
	sum.Requested = int(requested.Load())
	sum.Failed = int(failed.Load())

	logger.Info("restore-request finished",
		"total", sum.Total, "requested", sum.Requested, "in_progress", sum.InProgress,
		"available", sum.Available, "not_needed", sum.NotNeeded, "failed", sum.Failed, "dry_run", opts.DryRun)

	sum.API = client.APIStats()
	logAPIUsage(logger, sum.API)

	if sum.Failed > 0 {
		return sum, fmt.Errorf("%d object(s) failed", sum.Failed)
	}
	return sum, nil
}
