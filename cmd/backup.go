package cmd

import (
	"github.com/spf13/cobra"

	"backup-cse/internal/config"
	"backup-cse/internal/engine"
)

func newBackupCmd() *cobra.Command {
	var (
		dryRun   bool
		parallel int
		subpath  string
	)
	cmd := &cobra.Command{
		Use:   "backup <config.yaml> <source-name>",
		Short: "Encrypt and back up a source directory to S3",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}
			env, err := newRunEnv("backup")
			if err != nil {
				return err
			}
			defer env.Close()

			logPath := env.LogPath
			if dryRun {
				logPath = "" // dry-run ではログをアップロードしない
			}
			_, err = engine.Backup(cmd.Context(), engine.BackupOptions{
				Config:     cfg,
				SourceName: args[1],
				Subpath:    subpath,
				DryRun:     dryRun,
				Parallel:   parallel,
				Logger:     env.Logger,
				LogPath:    logPath,
				Progress:   env.Tracker,
			})
			return err
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be uploaded without uploading")
	cmd.Flags().IntVarP(&parallel, "parallel", "p", 4, "number of parallel uploads")
	cmd.Flags().StringVar(&subpath, "subpath", "", "restrict to a path (file or dir) under the source root; accepts a source-relative path or an absolute path inside the source")
	return cmd
}
