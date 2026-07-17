package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"backup-cse/internal/crypt"
)

func newKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen <key-file>",
		Short: "Generate a new 256-bit encryption key file",
		Long: `新しい 256bit の暗号鍵を生成し、base64 テキストとして <key-file> に保存します。
生成した鍵ファイルのパスを config.yaml の encryption.key-file に指定してください。

この鍵を失うとバックアップは一切復号できなくなります。安全な場所に
バックアップしてください(S3 上のバックアップとは別の場所に!)。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := args[0]
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists; refusing to overwrite an encryption key", path)
			}
			key, err := crypt.GenerateKey()
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(key.String()+"\n"), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"encryption key written to %s\nKeep this file safe: without it, backups cannot be decrypted.\n", path)
			return nil
		},
	}
}
