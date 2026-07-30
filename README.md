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
$ backup-cse backup          <config.yaml> <source-name> [--subpath P] [--dry-run] [-p N]
$ backup-cse restore         <config.yaml> <source-name> [<dest-dir>] [--subpath P] [--dry-run] [-p N]
$ backup-cse restore-request <config.yaml> <source-name> [--subpath P] [--tier standard|bulk] [--days N] [--dry-run]
$ backup-cse list            <config.yaml> <source-name> [--subpath P] [-l] [--json] [--dirs]
```

- `--dry-run` : 転送対象の一覧だけ表示し、S3 にもローカルにも書き込まない
- `-p, --parallel` : 並列転送数(デフォルト 4)
- `--subpath` : ソース配下の特定パス(ファイルまたはディレクトリ)だけを対象にする(下記「サブパスで対象を絞る」参照)
- `restore` の `<dest-dir>` を省略するとソースの `path` へ復元し、更新日時も復元される
- 実行中は端末に進捗が表示される。Ctrl-C で中断すると転送を止め、メタデータ(bbolt DB とログ)をアップロードしてから終了する(もう一度 Ctrl-C で強制終了)
- `storage-class` に `glacier` / `deep-archive` を指定したソースは、`restore` の前に `restore-request` で復元をリクエストしておく必要がある(下記「Glacier からのリストア」参照)

## サブパスで対象を絞る

`--subpath` を付けると、ソース(`sources.<name>.path`)配下の特定のパスだけを backup / restore / restore-request の対象にできます。大きなソースの一部だけを素早く扱いたいときに便利です。

```console
# 写真ソースのうち 2024/ 以下だけをバックアップ
$ backup-cse backup config.yaml photo --subpath 2024

# 2024/ 以下だけを別ディレクトリへ復元
$ backup-cse restore config.yaml photo ./restore-out --subpath 2024

# 単一ファイルだけを対象にすることも可能
$ backup-cse backup config.yaml photo --subpath 2024/jan/party.jpg
```

- 値は **ソースルート相対パス**(例 `2024/photos`)に加え、**ソース配下の絶対パス**(例 `/Users/aki/photo/2024/photos`)も指定できます。シェル補完が使えて便利です
- ソース外を指す絶対パスや、`..` でソースルートを抜ける指定はエラーになります
- `excludes` と併用でき、サブパス内でも除外パターンは有効です
- サブパスバックアップは**部分的な更新**です。対象外ファイルの S3 オブジェクトや秘匿名マッピング(bbolt)は変更されず、そのまま保持されます(削除の伝播はありません)
- `restore` / `restore-request` はサブパスに一致するファイルが1件も無い場合、警告を出して正常終了します

## バックアップ内容の確認(list)

別マシンでリストアするときは、そのマシンにオリジナルのディレクトリ構造が無いため、何がバックアップされているか・どの `--subpath` を指定すればよいかが分かりません。`list`(別名 `ls`)は S3 上の conceal DB を取得して、バックアップ済みのオリジナルパス一覧を表示します。**読み取り専用**で、S3 には何も書き込みません。

```console
# バックアップ済みのパス一覧
$ backup-cse list config.yaml photo
2024/feb.jpg
2024/jan/party.jpg
2023/old.jpg

# サイズと更新日時つき、2024/ 以下だけ
$ backup-cse list config.yaml photo -l --subpath 2024
2026-01-15T09:30:00Z     1.2 MiB  2024/feb.jpg
2026-02-03T14:00:00Z     3.4 MiB  2024/jan/party.jpg

# 機械可読(1行1JSON)。合計サマリは標準エラーに出るのでパイプを汚さない
$ backup-cse ls config.yaml photo --json | jq -r 'select(.size > 1e6) | .path'
```

- 一覧は**標準出力**、`N files, M dirs, total X` のサマリと診断は**標準エラー出力**に出ます(パイプや `--json | jq` で扱いやすい)
- `-l` : 各ファイルのサイズと更新日時(UTC・RFC3339)を表示
- `--json` : 1行1JSON(`path` / `is_dir` / `size` / `mtime`)
- `--dirs` : 空ディレクトリを含む登録済みディレクトリも一覧に含める(末尾 `/` で区別)
- `--subpath` : backup / restore と同じくソース配下の特定パスに絞り込み

## S3 上のレイアウト

```
<prefix><source-name>/
  data/<uuid>/<uuid>/...   実データ。パス全成分が UUID v4(内容も暗号化)
  metadata/conceal.db      名前の対応を保持する bbolt DB(内容は暗号化、名前は非秘匿)
  metadata/logs/*.ndjson   実行ログ(内容は暗号化、名前は非秘匿)
```

`metadata/` 配下は復元時にすぐ必要になるため、ソースのストレージクラス設定に関わらず常に STANDARD で保存されます。

## Glacier からのリストア

`storage-class` に `glacier`(Glacier Flexible Retrieval)や `deep-archive`(Glacier Deep Archive、`glacier-deep-archive` も可)を指定したソースは、アーカイブされたオブジェクトを直接ダウンロードできません。`restore` を実行する前に、まず復元リクエスト(restore request)を送って一時的な取り出しコピーを用意しておく必要があります。

```console
$ backup-cse restore-request config.yaml programs --tier standard   # 数時間で復元(既定)
$ backup-cse restore-request config.yaml programs --tier bulk       # 最大48時間、より安価
# ... 復元完了を待ってから ...
$ backup-cse restore config.yaml programs
```

- 復元リクエスト済み・復元完了済みのファイルはスキップされ、未着手のものにだけリクエストが送られる(何度実行しても安全)
- `--days` で一時コピーを保持する日数を指定できる(既定 7 日。この期間を過ぎると再びアーカイブ状態に戻り、再度 `restore-request` が必要)
- Deep Archive では `--tier expedited` は利用できない
- `standard` / `standard-ia` / `glacier-instant-retrieval` など即座に取得可能なストレージクラスのソースには何もしない
- `restore-request` を挟まずに `restore` を実行すると、アーカイブ済みで復元未完了のファイルは失敗として扱われる(他のファイルの処理は継続する)。`restore` はその件数を通常の失敗とは別に `not_restored` としてログ・終了時サマリに出力し、終了時のエラーメッセージで `restore-request` の実行を促す
- `RestoreObject` API の呼び出しには IAM ポリシーで `s3:RestoreObject` の許可が別途必要

## ログ

端末にはプレーンテキスト、`<UserCacheDir>/backup-cse/logs/`(macOS では `~/Library/Caches/backup-cse/logs/`)には改行区切り JSON で記録されます。バックアップ実行後、ログは暗号化して S3 の `metadata/logs/` にもアップロードされます。

### API 呼び出し回数の報告

各コマンドは終了時に、コストに影響する S3 リクエストの発行回数を `aws api usage (billable requests)` としてログに出力します。

- `put_list` : PUT / COPY / POST / LIST 系(高い方の課金クラス)。ファイル・conceal DB のアップロード、`ListObjectsV2`、マルチパートアップロードの各パートを含む
- `get` : GET / HEAD 系(安い方の課金クラス)。ダウンロードや `restore-request` の `HeadObject` など
- `restore` : `RestoreObject`(Glacier 取り出しリクエスト。別途データ取り出し料金もかかる)
- `billable_total` : 上記の合計。無料の `DeleteObject` 等は含まない

実際に S3 へ発行された操作単位で数えるため、大きなファイルのマルチパート分割や `ListObjectsV2` のページングも反映されます(リトライは二重計上しません)。

## 注意事項

- **削除は伝播しません**: ローカルで消したファイルは S3 に残ります(`aws s3 sync` の `--delete` 相当は未実装)
- **Glacier / Deep Archive からのリストア**は、事前に `restore-request` で復元を完了させておく必要があります(詳細は上記「Glacier からのリストア」参照)
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
