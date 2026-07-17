package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"backup-cse/internal/console"
	"backup-cse/internal/logging"
)

// runEnv は1回の実行で使うロギング・進捗表示のセットアップを保持する。
type runEnv struct {
	Logger  *slog.Logger
	Tracker *console.Tracker
	LogPath string

	printer *console.Printer
	logFile *os.File
}

// newRunEnv は端末用 Printer と NDJSON ログファイルを準備する。
// ログファイルは <UserCacheDir>/backup-cse/logs/ に残る。
func newRunEnv(subcommand string) (*runEnv, error) {
	printer := console.NewPrinter(os.Stderr)

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("resolve cache dir: %w", err)
	}
	logDir := filepath.Join(cacheDir, "backup-cse", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	logPath := filepath.Join(logDir,
		time.Now().UTC().Format("20060102T150405Z")+"-"+subcommand+".ndjson")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}

	return &runEnv{
		Logger:  logging.New(printer, logFile),
		Tracker: console.NewTracker(printer),
		LogPath: logPath,
		printer: printer,
		logFile: logFile,
	}, nil
}

func (e *runEnv) Close() {
	e.Tracker.Stop()
	e.printer.ClearProgress()
	e.logFile.Close()
}
