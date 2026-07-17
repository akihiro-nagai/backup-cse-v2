package cmd

import (
	"github.com/spf13/cobra"

	"backup-cse/internal/config"
	"backup-cse/internal/engine"
)

func newRestoreCmd() *cobra.Command {
	var (
		dryRun   bool
		parallel int
	)
	cmd := &cobra.Command{
		Use:   "restore <config.yaml> <source-name> [<dest-dir>]",
		Short: "Restore a backed-up source from S3",
		Long: `S3 上のバックアップからファイルを復元します。
<dest-dir> を省略すると設定ファイルのソース path へ復元します。`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}
			destDir := ""
			if len(args) == 3 {
				destDir = args[2]
			}
			env, err := newRunEnv("restore")
			if err != nil {
				return err
			}
			defer env.Close()

			_, err = engine.Restore(cmd.Context(), engine.RestoreOptions{
				Config:     cfg,
				SourceName: args[1],
				DestDir:    destDir,
				DryRun:     dryRun,
				Parallel:   parallel,
				Logger:     env.Logger,
				Progress:   env.Tracker,
			})
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be restored without writing files")
	cmd.Flags().IntVarP(&parallel, "parallel", "p", 4, "number of parallel downloads")
	return cmd
}
