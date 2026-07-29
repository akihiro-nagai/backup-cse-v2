package engine

import (
	"log/slog"

	"backup-cse/internal/storage"
)

// logAPIUsage はコストに効く S3 API 呼び出し回数をログに出力する。
// 無料の DELETE 等は billable_total に含めない。リトライは操作単位では
// 二重計上しない。
func logAPIUsage(logger *slog.Logger, api storage.BillableSummary) {
	logger.Info("aws api usage (billable requests)",
		"put_list", api.PutList,
		"get", api.Get,
		"restore", api.Restore,
		"billable_total", api.Total)
}
