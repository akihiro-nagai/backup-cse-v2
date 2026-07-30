// Package e2e は実際の S3 バケットを使った end-to-end テスト。
//
// 実行には以下の環境変数と AWS 認証情報(通常の認証チェーン)が必要:
//
//	BACKUP_CSE_E2E_BUCKET  テスト用バケット名(必須)
//	BACKUP_CSE_E2E_REGION  バケットのリージョン(必須)
//
// 未設定の場合テストはスキップされる。テストオブジェクトは
// backup-cse-e2e/<ランダム>/ プレフィクス以下に作られ、終了時に削除される。
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/google/uuid"

	"backup-cse/internal/config"
	"backup-cse/internal/crypt"
	"backup-cse/internal/engine"
	"backup-cse/internal/storage"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type e2eEnv struct {
	bucket string
	region string
	prefix string
	api    *s3.Client
	cfg    *config.Config
	srcDir string
}

func setup(t *testing.T) *e2eEnv {
	t.Helper()
	bucket := os.Getenv("BACKUP_CSE_E2E_BUCKET")
	region := os.Getenv("BACKUP_CSE_E2E_REGION")
	if bucket == "" || region == "" {
		t.Skip("set BACKUP_CSE_E2E_BUCKET and BACKUP_CSE_E2E_REGION to run E2E tests")
	}

	ctx := context.Background()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatal(err)
	}
	env := &e2eEnv{
		bucket: bucket,
		region: region,
		prefix: "backup-cse-e2e/" + uuid.NewString() + "/",
		api:    s3.NewFromConfig(awsCfg),
	}
	t.Cleanup(func() { env.deleteAll(t) })

	// テスト用ソースディレクトリ。
	env.srcDir = t.TempDir()
	mustWrite(t, filepath.Join(env.srcDir, "a.txt"), []byte("hello backup-cse"))
	big := make([]byte, crypt.ChunkSize+crypt.ChunkSize/2) // 複数チャンクを跨ぐサイズ
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(env.srcDir, "sub", "b.bin"), big)
	mustWrite(t, filepath.Join(env.srcDir, "node_modules", "dep", "skip.txt"), []byte("excluded"))
	if err := os.MkdirAll(filepath.Join(env.srcDir, "emptydir"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 鍵と設定ファイル。
	key, err := crypt.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	confDir := t.TempDir()
	mustWrite(t, filepath.Join(confDir, "backup.key"), []byte(key.String()+"\n"))
	confYAML := fmt.Sprintf(`
destination:
  bucket:
    bucket: %s
    prefix: %s
    region: %s

encryption:
  key-file: backup.key

sources:
  e2e:
    path: %s
    storage-class: standard
    excludes:
      - .*/node_modules/.*
`, bucket, env.prefix, region, env.srcDir)
	confPath := filepath.Join(confDir, "config.yaml")
	mustWrite(t, confPath, []byte(confYAML))
	t.Setenv(config.KeyEnvVar, "")

	env.cfg, err = config.Load(confPath)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *e2eEnv) listKeys(t *testing.T, sub string) []string {
	t.Helper()
	var keys []string
	p := s3.NewListObjectsV2Paginator(e.api, &s3.ListObjectsV2Input{
		Bucket:  aws.String(e.bucket),
		Prefix:  aws.String(e.prefix + sub),
		MaxKeys: aws.Int32(1000),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range page.Contents {
			keys = append(keys, strings.TrimPrefix(aws.ToString(o.Key), e.prefix+sub))
		}
	}
	return keys
}

func (e *e2eEnv) deleteAll(t *testing.T) {
	keys := e.listKeys(t, "")
	for len(keys) > 0 {
		n := min(len(keys), 1000)
		ids := make([]types.ObjectIdentifier, 0, n)
		for _, k := range keys[:n] {
			ids = append(ids, types.ObjectIdentifier{Key: aws.String(e.prefix + k)})
		}
		_, err := e.api.DeleteObjects(context.Background(), &s3.DeleteObjectsInput{
			Bucket: aws.String(e.bucket),
			Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(true)},
		})
		if err != nil {
			t.Errorf("cleanup: %v", err)
			return
		}
		keys = keys[n:]
	}
}

func (e *e2eEnv) backup(t *testing.T, dryRun bool) engine.BackupSummary {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "log.ndjson")
	mustWrite(t, logPath, nil)
	sum, err := engine.Backup(context.Background(), engine.BackupOptions{
		Config:     e.cfg,
		SourceName: "e2e",
		DryRun:     dryRun,
		Parallel:   4,
		LogPath:    logPath,
	})
	if err != nil {
		t.Fatalf("backup: %v (summary %+v)", err, sum)
	}
	return sum
}

func (e *e2eEnv) restore(t *testing.T, dest string, dryRun bool) engine.RestoreSummary {
	t.Helper()
	sum, err := engine.Restore(context.Background(), engine.RestoreOptions{
		Config:     e.cfg,
		SourceName: "e2e",
		DestDir:    dest,
		DryRun:     dryRun,
		Parallel:   4,
	})
	if err != nil {
		t.Fatalf("restore: %v (summary %+v)", err, sum)
	}
	return sum
}

func (e *e2eEnv) restoreRequest(t *testing.T, tier string, dryRun bool) engine.RestoreRequestSummary {
	t.Helper()
	sum, err := engine.RestoreRequest(context.Background(), engine.RestoreRequestOptions{
		Config:     e.cfg,
		SourceName: "e2e",
		Tier:       tier,
		Days:       1,
		DryRun:     dryRun,
		Parallel:   4,
	})
	if err != nil {
		t.Fatalf("restore-request: %v (summary %+v)", err, sum)
	}
	return sum
}

// TestRestoreRequestSkipsStandardStorageE2E は、Standard クラスのソースに
// 対して restore-request を実行した場合、復元リクエストが一切発生せず
// 全ファイルが NotNeeded と判定されることを確認する。
// この e2e ソース (config.example.yaml 参照) は storage-class: standard な
// ので、実際に Glacier へアーカイブして数時間〜48時間待つ本格的な検証は
// 現実的でない。ここでは HeadObject 経由の判定ロジックが実 S3 に対して
// 正しく動くことだけを検証する。
func TestRestoreRequestSkipsStandardStorageE2E(t *testing.T) {
	env := setup(t)
	env.backup(t, false)

	sum := env.restoreRequest(t, "standard", true)
	if sum.Total != 2 || sum.NotNeeded != 2 || sum.Requested != 0 || sum.Failed != 0 {
		t.Fatalf("restore-request summary for standard-class source: %+v", sum)
	}

	sum = env.restoreRequest(t, "standard", false)
	if sum.Total != 2 || sum.NotNeeded != 2 || sum.Requested != 0 || sum.Failed != 0 {
		t.Fatalf("restore-request (non-dry-run) summary for standard-class source: %+v", sum)
	}
}

func TestBackupAndRestoreE2E(t *testing.T) {
	env := setup(t)

	// --- dry-run では何もアップロードされない ---
	sum := env.backup(t, true)
	if sum.Candidates != 2 || sum.Uploaded != 0 {
		t.Fatalf("dry-run summary: %+v", sum)
	}
	if keys := env.listKeys(t, ""); len(keys) != 0 {
		t.Fatalf("dry-run uploaded objects: %v", keys)
	}

	// --- 初回バックアップ ---
	sum = env.backup(t, false)
	if sum.Scanned != 2 || sum.Uploaded != 2 || sum.Failed != 0 {
		t.Fatalf("first backup summary: %+v", sum)
	}
	// コスト対象 API 回数が計上されていること。少なくとも
	// ListObjectsV2 1 回 + PutObject(2 ファイル + conceal db)3 回。
	if sum.API.PutList < 4 {
		t.Errorf("expected >=4 PUT/LIST requests, got %d (%+v)", sum.API.PutList, sum.API.ByOp)
	}
	if sum.API.Total == 0 {
		t.Errorf("billable API total was not counted: %+v", sum.API)
	}

	// data/ 以下のキーは全成分が UUID v4 で、オリジナル名を含まない。
	dataKeys := env.listKeys(t, "e2e/data/")
	if len(dataKeys) != 2 {
		t.Fatalf("data keys = %v", dataKeys)
	}
	for _, k := range dataKeys {
		for _, comp := range strings.Split(k, "/") {
			if !uuidRe.MatchString(comp) {
				t.Errorf("data key component %q is not a uuid v4 (key %s)", comp, k)
			}
		}
	}

	// metadata/ には conceal.db とログが(名前を秘匿せず)置かれる。
	metaKeys := env.listKeys(t, "e2e/metadata/")
	var haveDB, haveLog bool
	for _, k := range metaKeys {
		if k == "conceal.db" {
			haveDB = true
		}
		if strings.HasPrefix(k, "logs/") && strings.HasSuffix(k, "-backup.ndjson") {
			haveLog = true
		}
	}
	if !haveDB || !haveLog {
		t.Fatalf("metadata keys = %v", metaKeys)
	}

	// オブジェクトメタデータに更新日時とサイズが記録され、内容は暗号化されている。
	obj, err := env.api.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(env.bucket),
		Key:    aws.String(env.prefix + "e2e/data/" + dataKeys[0]),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 64)
	n, _ := obj.Body.Read(body)
	obj.Body.Close()
	if obj.Metadata[storage.MetaMTime] == "" || obj.Metadata[storage.MetaSize] == "" {
		t.Errorf("object metadata missing: %v", obj.Metadata)
	}
	if bytes.Contains(body[:n], []byte("hello backup-cse")) {
		t.Error("object body contains plaintext")
	}

	// --- 変更なしの再バックアップは何もしない ---
	sum = env.backup(t, false)
	if sum.Uploaded != 0 || sum.Skipped != 2 {
		t.Fatalf("second backup summary: %+v", sum)
	}

	// --- 1ファイル変更後は差分のみアップロード ---
	changed := filepath.Join(env.srcDir, "a.txt")
	mustWrite(t, changed, []byte("hello again, now modified"))
	newMTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(changed, newMTime, newMTime); err != nil {
		t.Fatal(err)
	}
	sum = env.backup(t, false)
	if sum.Uploaded != 1 || sum.Skipped != 1 {
		t.Fatalf("incremental backup summary: %+v", sum)
	}
	// 変更ファイルは同じ秘匿化キーを上書きするためオブジェクト数は変わらない。
	if keys := env.listKeys(t, "e2e/data/"); len(keys) != 2 {
		t.Fatalf("data keys after modify = %v", keys)
	}

	// --- フルリストア ---
	dest := t.TempDir()
	rsum := env.restore(t, dest, false)
	if rsum.Total != 2 || rsum.Restored != 2 || rsum.Failed != 0 {
		t.Fatalf("restore summary: %+v", rsum)
	}
	assertSameFile(t, filepath.Join(env.srcDir, "a.txt"), filepath.Join(dest, "a.txt"))
	assertSameFile(t, filepath.Join(env.srcDir, "sub", "b.bin"), filepath.Join(dest, "sub", "b.bin"))
	if _, err := os.Stat(filepath.Join(dest, "node_modules")); !os.IsNotExist(err) {
		t.Error("excluded node_modules was restored")
	}
	if fi, err := os.Stat(filepath.Join(dest, "emptydir")); err != nil || !fi.IsDir() {
		t.Error("empty directory was not restored")
	}

	// --- 差分リストア: 最新のファイルはスキップされる ---
	rsum = env.restore(t, dest, false)
	if rsum.Restored != 0 || rsum.Skipped != 2 {
		t.Fatalf("second restore summary: %+v", rsum)
	}

	// 1ファイル消すとそれだけ復元される。
	if err := os.Remove(filepath.Join(dest, "a.txt")); err != nil {
		t.Fatal(err)
	}
	rsum = env.restore(t, dest, false)
	if rsum.Restored != 1 || rsum.Skipped != 1 {
		t.Fatalf("partial restore summary: %+v", rsum)
	}
}

// TestBackupRestoreSubpathE2E は --subpath でソース配下の一部だけを
// バックアップ・リストアできること、および絶対パス指定・範囲外指定の
// 挙動を実 S3 に対して検証する。
func TestBackupRestoreSubpathE2E(t *testing.T) {
	env := setup(t)

	// subpath "sub" 配下(sub/b.bin の 1 ファイル)だけをバックアップする。
	sum, err := engine.Backup(context.Background(), engine.BackupOptions{
		Config:     env.cfg,
		SourceName: "e2e",
		Subpath:    "sub",
		Parallel:   4,
	})
	if err != nil {
		t.Fatalf("subpath backup: %v (%+v)", err, sum)
	}
	if sum.Scanned != 1 || sum.Uploaded != 1 || sum.Failed != 0 {
		t.Fatalf("subpath backup summary: %+v", sum)
	}
	if keys := env.listKeys(t, "e2e/data/"); len(keys) != 1 {
		t.Fatalf("expected exactly 1 data object after subpath backup, got %v", keys)
	}

	// subpath "sub" でリストア → sub/b.bin だけが復元され、a.txt は復元されない。
	dest := t.TempDir()
	rsum, err := engine.Restore(context.Background(), engine.RestoreOptions{
		Config:     env.cfg,
		SourceName: "e2e",
		DestDir:    dest,
		Subpath:    "sub",
		Parallel:   4,
	})
	if err != nil {
		t.Fatalf("subpath restore: %v (%+v)", err, rsum)
	}
	if rsum.Total != 1 || rsum.Restored != 1 || rsum.Failed != 0 {
		t.Fatalf("subpath restore summary: %+v", rsum)
	}
	assertSameFile(t, filepath.Join(env.srcDir, "sub", "b.bin"), filepath.Join(dest, "sub", "b.bin"))
	if _, err := os.Stat(filepath.Join(dest, "a.txt")); !os.IsNotExist(err) {
		t.Error("a.txt should not be restored under subpath 'sub'")
	}

	// ソース配下の絶対パス指定でも同じスコープになる(変更なしなのでアップロード 0)。
	sum, err = engine.Backup(context.Background(), engine.BackupOptions{
		Config:     env.cfg,
		SourceName: "e2e",
		Subpath:    filepath.Join(env.srcDir, "sub"),
		Parallel:   4,
	})
	if err != nil {
		t.Fatalf("absolute subpath backup: %v (%+v)", err, sum)
	}
	if sum.Scanned != 1 || sum.Uploaded != 0 || sum.Skipped != 1 {
		t.Fatalf("absolute subpath backup summary: %+v", sum)
	}

	// ソース外の絶対パスはエラー。
	if _, err := engine.Backup(context.Background(), engine.BackupOptions{
		Config:     env.cfg,
		SourceName: "e2e",
		Subpath:    t.TempDir(),
		Parallel:   4,
	}); err == nil {
		t.Fatal("expected error for subpath outside the source root")
	}
}

// TestListE2E は backup 後に list(conceal DB のプレビュー)がオリジナルパスを
// 正しく返すこと、subpath 絞り込みが効くことを実 S3 に対して検証する。
func TestListE2E(t *testing.T) {
	env := setup(t)
	env.backup(t, false)

	res, err := engine.List(context.Background(), engine.ListOptions{
		Config:     env.cfg,
		SourceName: "e2e",
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	// node_modules は excludes、emptydir はファイルを持たないので一覧は 2 件。
	got := map[string]engine.ListEntry{}
	for _, e := range res.Entries {
		got[e.Path] = e
	}
	if res.Files != 2 || len(got) != 2 {
		t.Fatalf("list entries = %+v", res.Entries)
	}
	if _, ok := got["a.txt"]; !ok {
		t.Error("a.txt not listed")
	}
	if e, ok := got["sub/b.bin"]; !ok {
		t.Error("sub/b.bin not listed")
	} else if e.Size <= 0 || e.MTime.IsZero() {
		t.Errorf("sub/b.bin metadata missing: %+v", e)
	}

	// subpath 絞り込み。
	res, err = engine.List(context.Background(), engine.ListOptions{
		Config:     env.cfg,
		SourceName: "e2e",
		Subpath:    "sub",
	})
	if err != nil {
		t.Fatalf("list subpath: %v", err)
	}
	if res.Files != 1 || res.Entries[0].Path != "sub/b.bin" {
		t.Fatalf("subpath list = %+v", res.Entries)
	}

	// --dirs で登録済みディレクトリ(emptydir 含む)が現れる。
	res, err = engine.List(context.Background(), engine.ListOptions{
		Config:      env.cfg,
		SourceName:  "e2e",
		IncludeDirs: true,
	})
	if err != nil {
		t.Fatalf("list dirs: %v", err)
	}
	var haveEmpty bool
	for _, e := range res.Entries {
		if e.Path == "emptydir" && e.IsDir {
			haveEmpty = true
		}
	}
	if !haveEmpty {
		t.Errorf("emptydir not listed with IncludeDirs: %+v", res.Entries)
	}
}

func assertSameFile(t *testing.T, want, got string) {
	t.Helper()
	wb, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	gb, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wb, gb) {
		t.Errorf("content mismatch: %s vs %s", want, got)
	}
	wi, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	gi, err := os.Stat(got)
	if err != nil {
		t.Fatal(err)
	}
	if wi.ModTime().UnixNano() != gi.ModTime().UnixNano() {
		t.Errorf("mtime mismatch: %s=%v vs %s=%v", want, wi.ModTime(), got, gi.ModTime())
	}
}
