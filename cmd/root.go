// Package cmd は backup-cse の CLI を実装する。
package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

// version はリリースビルド時に ldflags で埋め込まれる。
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "backup-cse",
		Short: "Client-side encrypted backup to Amazon S3",
		Long: `backup-cse はローカルディレクトリをクライアントサイドで暗号化して
Amazon S3 へ差分バックアップ・リストアする CLI ツールです。
ファイル名・ディレクトリ名は UUID v4 で秘匿化されます。`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newBackupCmd(), newRestoreCmd(), newRestoreRequestCmd(), newListCmd(), newKeygenCmd())
	return root
}

// Execute は CLI を実行する。
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}
