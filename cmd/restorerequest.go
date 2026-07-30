package cmd

import (
	"github.com/spf13/cobra"

	"backup-cse/internal/config"
	"backup-cse/internal/engine"
)

func newRestoreRequestCmd() *cobra.Command {
	var (
		tier     string
		days     int
		dryRun   bool
		parallel int
		subpath  string
	)
	cmd := &cobra.Command{
		Use:   "restore-request <config.yaml> <source-name>",
		Short: "Request S3 Glacier restoration of an archived source",
		Long: `S3 Glacier Flexible Retrieval / S3 Glacier Deep Archive に保存したファイルは
そのままでは取得できず、事前に復元リクエスト(restore request)を送っておく
必要があります。このコマンドはソース内の全ファイルを確認し、まだ復元
リクエストされていないものにリクエストを送ります。

復元には --tier standard で数時間、--tier bulk で最大48時間程度かかります
(Deep Archive では expedited は使えません)。復元が完了したら
"backup-cse restore" を実行してください。

standard-ia や glacier-instant-retrieval など、即座に取得可能なストレージ
クラスのソースに対しては何もする必要がなく、このコマンドも何もしません。`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}
			env, err := newRunEnv("restore-request")
			if err != nil {
				return err
			}
			defer env.Close()

			_, err = engine.RestoreRequest(cmd.Context(), engine.RestoreRequestOptions{
				Config:     cfg,
				SourceName: args[1],
				Tier:       tier,
				Days:       int32(days),
				Subpath:    subpath,
				DryRun:     dryRun,
				Parallel:   parallel,
				Logger:     env.Logger,
			})
			return err
		},
	}
	cmd.Flags().StringVar(&tier, "tier", "standard", "restore tier: standard or bulk (expedited is not supported for deep-archive)")
	cmd.Flags().IntVar(&days, "days", 7, "number of days the restored copy stays available before reverting to archive")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be requested without calling S3")
	cmd.Flags().IntVarP(&parallel, "parallel", "p", 4, "number of parallel S3 requests")
	cmd.Flags().StringVar(&subpath, "subpath", "", "restrict to a path (file or dir) under the source root; accepts a source-relative path or an absolute path inside the source")
	return cmd
}
