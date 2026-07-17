# backup-cse

ローカルディスクの内容をクライアントサイドで暗号化して Amazon S3 へ差分バックアップ・リストアする CLI ツール。

- **クライアントサイド暗号化**: AES-256-GCM のチャンクストリーム方式。S3 には暗号文しか送られず、改ざん・切り詰めは復号時に検知される
- **ファイル名の秘匿化 (concealment)**: S3 上のファイル名・ディレクトリ名はすべて UUID v4。オリジナル名との対応は bbolt DB に保存され、DB 自体も暗号化して S3 に置かれる
- **差分バックアップ / リストア**: `aws s3 sync` と同様にサイズ + 更新日時で変更を検出。S3 のオブジェクト一覧は ListObjectsV2 で 1000 件ずつ取得しコストを最小化
- **CGO 不使用のシングルバイナリ** (Linux / Windows / macOS)

## ビルド

```console
$ go build -o backup-cse .          # Go 1.26.5 (mise.toml 参照)
$ CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o backup-cse-linux .
```

## セットアップ

```console
$ backup-cse keygen backup.key      # 256bit 鍵を生成
$ cp config.example.yaml config.yaml
$ $EDITOR config.yaml
```

> **警告**: `backup.key` を失うとバックアップは一切復号できません。
> S3 上のバックアップとは別の安全な場所に必ず控えを保管してください。

鍵は設定の `encryption.key-file` か、環境変数 `BACKUP_CSE_KEY`(base64)で渡します。
AWS 認証情報は通常の認証チェーン(環境変数、`~/.aws/credentials`、IAM ロール等)を使います。

## 使い方

```console
$ backup-cse backup  <config.yaml> <source-name> [--dry-run] [-p N]
$ backup-cse restore <config.yaml> <source-name> [<dest-dir>] [--dry-run] [-p N]
```

- `--dry-run` : 転送対象の一覧だけ表示し、S3 にもローカルにも書き込まない
- `-p, --parallel` : 並列転送数(デフォルト 4)
- `restore` の `<dest-dir>` を省略するとソースの `path` へ復元し、更新日時も復元される
- 実行中は端末に進捗が表示される。Ctrl-C で中断すると転送を止め、メタデータ(bbolt DB とログ)をアップロードしてから終了する(もう一度 Ctrl-C で強制終了)

## S3 上のレイアウト

```
<prefix><source-name>/
  data/<uuid>/<uuid>/...   実データ。パス全成分が UUID v4(内容も暗号化)
  metadata/conceal.db      名前の対応を保持する bbolt DB(内容は暗号化、名前は非秘匿)
  metadata/logs/*.ndjson   実行ログ(内容は暗号化、名前は非秘匿)
```

`metadata/` 配下は復元時にすぐ必要になるため、ソースのストレージクラス設定に関わらず常に STANDARD で保存されます。

## ログ

端末にはプレーンテキスト、`<UserCacheDir>/backup-cse/logs/`(macOS では `~/Library/Caches/backup-cse/logs/`)には改行区切り JSON で記録されます。バックアップ実行後、ログは暗号化して S3 の `metadata/logs/` にもアップロードされます。

## 注意事項

- **削除は伝播しません**: ローカルで消したファイルは S3 に残ります(`aws s3 sync` の `--delete` 相当は未実装)
- **Glacier / Deep Archive からのリストア**は、S3 側で事前に restore リクエストを完了させておく必要があります。未リストアのオブジェクトはエラーとしてログに記録されます
- 除外パターンはスラッシュ区切りの**フルパス**に対する Go 正規表現です(例: `.*/node_modules/.*`)
- ストレージクラス名の `glacier-deep-archive` は S3 の `DEEP_ARCHIVE` にマッピングされます

## テスト

```console
$ go test ./...                     # 単体テスト
$ BACKUP_CSE_E2E_BUCKET=my-test-bucket \
  BACKUP_CSE_E2E_REGION=us-west-2 \
  go test ./e2e -v                  # 実 S3 を使う E2E テスト
```

E2E テストは `backup-cse-e2e/<ランダム>/` プレフィクス以下にオブジェクトを作成し、終了時に削除します。環境変数が未設定の場合はスキップされます。
