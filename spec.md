ローカルディスクの内容をS3にクライアントサイドで暗号化してバックアップするCLIツールを作ります
言語: Go 1.26.5
サブコマンド実装: cobra
オリジナル<=>秘匿ファイル名保存DB: bbolt

使い方:
  $ backup-cse backup <config.yaml> <source-name> [--dry-run]
  $ backup-cse restore <config.yaml> <source-name> [<dest-dir>] [--dry-run]

config.yaml で以下のような形式

```
destination:
  bucket:
    bucket: my-backup-backet
    prefix: my-macbook-backups/
    region: us-west-2

sources:
  photo:
    path: /Users/aki/photo
    storage-class: standard
  programs:
    path: /Users/aki/programs
    storage-class: gracier-deep-archive
    excludes:
      - .*/node_modules/.*
```

機能:
 - クライアントサイドでファイル内容を暗号化し、s3 へバックアップする
 - s3 メタデータにファイルの更新日時とファイルサイズを記録し、ローカルで変更があったもののみバックアップとリストアができるようにする (aws s3 sync と似た方式)
 - s3 ストレージクラスを sourceごとに指定可能
 - リストア時にファイルの更新日時等を復元する
 - ファイル名・ディレクトリ名を秘匿する
   - uuid v4 で新しいファイル名・ディレクトリ名にし、オリジナルのファイル名は bbolt に保存
 - ディレクトリ構造は維持する
 - バックアップ先S3のディレクトリ構造は
   <source-name>/metadata にログや、bbolt のDBを入れ
   <source-name>/data に実際のバックアップデータを入れる
   metadata は内容は暗号化するが、ファイル名の秘匿化はしない
 - dry-run 可能
 - 並列でバックアップ可能。並列度を変更可能。
 - ログは端末では plain text, ファイルに改行区切りjsonで記録。バックアップ後、暗号化し、s3 へも記録する
 - 進捗表示をする

実装上の注意:
 - CGO は使わず、Goネイティブ・シングルバイナリの成果物となること
 - Linux, Windows, MacOS のマルチプラットフォーム対応をすること
 - 秘匿化は concealment という単語を使う。暗号化は encrypt
 - 差分バックアップ・リストアでは ListObjectV2 で 1000件ずつオブジェクトの状態を取得しコストを最小化する
 - シグナルハンドラを実装し、安全に終了処理をする
   - 終了時にログとbboltをアップロードし、s3上とbboltDBで不整合が起きないようにする
 - 適切にパッケージを分ける
 - テストを実装する。単体テストと、E2Eテストの二種類。E2Eでは実際にS3を使う。

