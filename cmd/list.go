package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"backup-cse/internal/config"
	"backup-cse/internal/console"
	"backup-cse/internal/engine"
	"backup-cse/internal/logging"
)

func newListCmd() *cobra.Command {
	var (
		subpath     string
		long        bool
		asJSON      bool
		includeDirs bool
	)
	cmd := &cobra.Command{
		Use:     "list <config.yaml> <source-name>",
		Aliases: []string{"ls"},
		Short:   "List the original paths recorded in a backup",
		Long: `S3 上の conceal DB を取得し、バックアップ済みのオリジナルパス一覧を表示します。
別マシンでリストアする前に、何がバックアップされているか、どの --subpath を
指定すればよいかを確認するのに使えます。

読み取り専用で S3 には何も書き込みません。一覧は標準出力へ、サマリや診断は
標準エラー出力へ出力するため、パイプで扱えます。`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(args[0])
			if err != nil {
				return err
			}
			// 読み取り専用コマンドなのでログファイル生成・アップロードは行わず、
			// 診断は標準エラー出力のみに出す。
			logger := logging.New(console.NewPrinter(os.Stderr), nil)

			res, err := engine.List(cmd.Context(), engine.ListOptions{
				Config:      cfg,
				SourceName:  args[1],
				Subpath:     subpath,
				IncludeDirs: includeDirs,
				Logger:      logger,
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if asJSON {
				if err := writeJSON(out, res.Entries); err != nil {
					return err
				}
			} else if long {
				writeLong(out, res.Entries)
			} else {
				writePlain(out, res.Entries)
			}

			fmt.Fprintf(os.Stderr, "%d files, %d dirs, total %s\n",
				res.Files, res.Dirs, console.HumanBytes(res.TotalBytes))
			return nil
		},
	}
	cmd.Flags().StringVar(&subpath, "subpath", "", "restrict to a path (file or dir) under the source root; accepts a source-relative path or an absolute path inside the source")
	cmd.Flags().BoolVarP(&long, "long", "l", false, "show size and modification time for each entry")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output one JSON object per line")
	cmd.Flags().BoolVar(&includeDirs, "dirs", false, "include registered (possibly empty) directories")
	return cmd
}

func displayPath(e engine.ListEntry) string {
	if e.IsDir {
		return e.Path + "/"
	}
	return e.Path
}

func writePlain(w io.Writer, entries []engine.ListEntry) {
	for _, e := range entries {
		fmt.Fprintln(w, displayPath(e))
	}
}

func writeLong(w io.Writer, entries []engine.ListEntry) {
	for _, e := range entries {
		mtime, size := "-", "-"
		if !e.IsDir {
			mtime = e.MTime.UTC().Format(time.RFC3339)
			size = console.HumanBytes(e.Size)
		}
		fmt.Fprintf(w, "%-20s  %10s  %s\n", mtime, size, displayPath(e))
	}
}

func writeJSON(w io.Writer, entries []engine.ListEntry) error {
	type jsonEntry struct {
		Path  string `json:"path"`
		IsDir bool   `json:"is_dir"`
		Size  int64  `json:"size"`
		MTime string `json:"mtime,omitempty"`
	}
	enc := json.NewEncoder(w)
	for _, e := range entries {
		je := jsonEntry{Path: e.Path, IsDir: e.IsDir, Size: e.Size}
		if !e.IsDir {
			je.MTime = e.MTime.UTC().Format(time.RFC3339Nano)
		}
		if err := enc.Encode(je); err != nil {
			return err
		}
	}
	return nil
}
