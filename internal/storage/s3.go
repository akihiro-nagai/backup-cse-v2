// Package storage は S3 への暗号化アップロード・ダウンロードを提供する。
// アップロードされる全オブジェクトの内容は crypt パッケージで暗号化される。
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"backup-cse/internal/crypt"
)

// S3 ユーザーメタデータのキー。差分判定・リストア用にオリジナルの
// 更新日時とサイズを記録する(ファイル名は含めない)。
const (
	MetaMTime = "mtime-unix-nano"
	MetaSize  = "original-size"
)

// ErrNotFound はオブジェクトが存在しないことを表す。
var ErrNotFound = errors.New("storage: object not found")

// ErrNotRestored はオブジェクトが Glacier 系ストレージクラスにアーカイブされて
// いて、復元リクエスト(restore request)が未完了のため取得できないことを表す。
var ErrNotRestored = errors.New("storage: object is archived; run 'backup-cse restore-request' first")

// Client は特定の bucket/prefix に対する暗号化 S3 クライアント。
type Client struct {
	api      *s3.Client
	uploader *manager.Uploader
	bucket   string
	prefix   string
	key      crypt.Key
}

// New はデフォルトの AWS 認証チェーンを使ってクライアントを作る。
// prefix は空か "/" 終わりであること。
func New(ctx context.Context, region, bucket, prefix string, key crypt.Key) (*Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	api := s3.NewFromConfig(cfg)
	return &Client{
		api:      api,
		uploader: manager.NewUploader(api),
		bucket:   bucket,
		prefix:   prefix,
		key:      key,
	}, nil
}

// ListKeys は prefix+subPrefix 以下の全オブジェクトキー(subPrefix からの相対)を
// ListObjectsV2 で 1000 件ずつ取得して返す。
func (c *Client) ListKeys(ctx context.Context, subPrefix string) (map[string]struct{}, error) {
	full := c.prefix + subPrefix
	out := make(map[string]struct{})
	p := s3.NewListObjectsV2Paginator(c.api, &s3.ListObjectsV2Input{
		Bucket:  aws.String(c.bucket),
		Prefix:  aws.String(full),
		MaxKeys: aws.Int32(1000),
	})
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list s3://%s/%s: %w", c.bucket, full, err)
		}
		for _, obj := range page.Contents {
			rel := strings.TrimPrefix(aws.ToString(obj.Key), full)
			if rel == "" {
				continue
			}
			out[rel] = struct{}{}
		}
	}
	return out, nil
}

// Upload は src を暗号化して prefix+key へアップロードする。
func (c *Client) Upload(ctx context.Context, key string, src io.Reader, class types.StorageClass, meta map[string]string) error {
	enc := crypt.EncryptingReader(src, c.key)
	defer enc.Close()
	_, err := c.uploader.Upload(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(c.bucket),
		Key:          aws.String(c.prefix + key),
		Body:         enc,
		StorageClass: class,
		Metadata:     meta,
	})
	if err != nil {
		return fmt.Errorf("upload s3://%s/%s%s: %w", c.bucket, c.prefix, key, err)
	}
	return nil
}

// Download は prefix+key を取得・復号して dst へ書き込む。
// オブジェクトが存在しない場合は ErrNotFound を返す。
func (c *Client) Download(ctx context.Context, key string, dst io.Writer) error {
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(c.prefix + key),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return fmt.Errorf("%w: s3://%s/%s%s", ErrNotFound, c.bucket, c.prefix, key)
		}
		var ios *types.InvalidObjectState
		if errors.As(err, &ios) {
			return fmt.Errorf("%w: s3://%s/%s%s", ErrNotRestored, c.bucket, c.prefix, key)
		}
		return fmt.Errorf("download s3://%s/%s%s: %w", c.bucket, c.prefix, key, err)
	}
	defer out.Body.Close()
	if err := crypt.Decrypt(dst, out.Body, c.key); err != nil {
		return fmt.Errorf("decrypt s3://%s/%s%s: %w", c.bucket, c.prefix, key, err)
	}
	return nil
}

// DownloadToFile は Download の結果をファイルに保存する。
func (c *Client) DownloadToFile(ctx context.Context, key, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := c.Download(ctx, key, f); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

// RestoreState はアーカイブ済みオブジェクトの復元(restore request)状態。
type RestoreState int

const (
	// RestoreNotNeeded は Glacier 系ストレージクラスではなく、復元リクエスト
	// 無しで即座にダウンロードできることを示す。
	RestoreNotNeeded RestoreState = iota
	// RestoreNotRequested はアーカイブされているが、まだ復元リクエストが
	// 行われていない(または有効期限切れの)ことを示す。
	RestoreNotRequested
	// RestoreInProgress は復元リクエスト済みで完了を待っていることを示す。
	RestoreInProgress
	// RestoreAvailable は復元が完了し、一時的にダウンロード可能なことを示す。
	RestoreAvailable
)

func (s RestoreState) String() string {
	switch s {
	case RestoreNotNeeded:
		return "not-needed"
	case RestoreNotRequested:
		return "not-requested"
	case RestoreInProgress:
		return "in-progress"
	case RestoreAvailable:
		return "available"
	default:
		return "unknown"
	}
}

// ObjectStatus は HeadObject で得られるオブジェクトの復元関連ステータス。
type ObjectStatus struct {
	StorageClass types.StorageClass
	Restore      RestoreState
}

// NeedsRestore は class/archiveStatus のオブジェクトが GetObject の前に
// 復元リクエストを必要とするかどうかを判定する。
// S3 Glacier Flexible Retrieval・S3 Glacier Deep Archive のオブジェクトに加え、
// S3 Intelligent-Tiering が自動的にアーカイブ層へ移行させたオブジェクトも対象になる。
func NeedsRestore(class types.StorageClass, archiveStatus types.ArchiveStatus) bool {
	switch class {
	case types.StorageClassGlacier, types.StorageClassDeepArchive:
		return true
	}
	switch archiveStatus {
	case types.ArchiveStatusArchiveAccess, types.ArchiveStatusDeepArchiveAccess:
		return true
	}
	return false
}

// restoreHeaderExpiry は HeadObject の Restore ヘッダから expiry-date を取り出す。
// 例: `ongoing-request="false", expiry-date="Fri, 21 Dec 2012 00:00:00 GMT"`
var restoreHeaderExpiry = regexp.MustCompile(`expiry-date="([^"]+)"`)

// classifyRestore は HeadObject の結果から RestoreState を判定する。
func classifyRestore(class types.StorageClass, archiveStatus types.ArchiveStatus, restoreHeader string, now time.Time) RestoreState {
	if !NeedsRestore(class, archiveStatus) {
		return RestoreNotNeeded
	}
	if restoreHeader == "" {
		return RestoreNotRequested
	}
	if strings.Contains(restoreHeader, `ongoing-request="true"`) {
		return RestoreInProgress
	}
	if m := restoreHeaderExpiry.FindStringSubmatch(restoreHeader); m != nil {
		if t, err := time.Parse(time.RFC1123, m[1]); err == nil && now.Before(t) {
			return RestoreAvailable
		}
	}
	// 復元期限切れ、またはヘッダを解釈できない場合は再リクエストが必要とみなす。
	return RestoreNotRequested
}

// Stat は prefix+key のストレージクラスと復元状態を取得する。
// オブジェクトが存在しない場合は ErrNotFound を返す。
func (c *Client) Stat(ctx context.Context, key string) (ObjectStatus, error) {
	out, err := c.api.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(c.prefix + key),
	})
	if err != nil {
		var nf *types.NotFound
		if errors.As(err, &nf) {
			return ObjectStatus{}, fmt.Errorf("%w: s3://%s/%s%s", ErrNotFound, c.bucket, c.prefix, key)
		}
		return ObjectStatus{}, fmt.Errorf("head s3://%s/%s%s: %w", c.bucket, c.prefix, key, err)
	}
	return ObjectStatus{
		StorageClass: out.StorageClass,
		Restore:      classifyRestore(out.StorageClass, out.ArchiveStatus, aws.ToString(out.Restore), time.Now()),
	}, nil
}

// RequestRestore は prefix+key の一時的な復元(restore request)をリクエストする。
// tier・days の詳細は https://docs.aws.amazon.com/AmazonS3/latest/userguide/restoring-objects-retrieval-options.html を参照。
func (c *Client) RequestRestore(ctx context.Context, key string, tier types.Tier, days int32) error {
	_, err := c.api.RestoreObject(ctx, &s3.RestoreObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(c.prefix + key),
		RestoreRequest: &types.RestoreRequest{
			Days:                 aws.Int32(days),
			GlacierJobParameters: &types.GlacierJobParameters{Tier: tier},
		},
	})
	if err != nil {
		return fmt.Errorf("restore-request s3://%s/%s%s: %w", c.bucket, c.prefix, key, err)
	}
	return nil
}

// ParseTier は設定文字列を復元(restore request)の取得ティアへ変換する。
// 空文字は Standard。Deep Archive では Expedited は利用できない。
func ParseTier(s string) (types.Tier, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "standard":
		return types.TierStandard, nil
	case "bulk":
		return types.TierBulk, nil
	case "expedited":
		return types.TierExpedited, nil
	default:
		return "", fmt.Errorf("unknown restore tier %q (want standard, bulk, or expedited)", s)
	}
}

// ParseStorageClass は設定文字列を S3 ストレージクラスへ変換する。
// 空文字は STANDARD。"glacier-deep-archive" のような別名も受け付ける。
func ParseStorageClass(s string) (types.StorageClass, error) {
	if strings.TrimSpace(s) == "" {
		return types.StorageClassStandard, nil
	}
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), "-", "_"))
	switch n {
	case "GLACIER_DEEP_ARCHIVE":
		n = string(types.StorageClassDeepArchive)
	case "GLACIER_FLEXIBLE_RETRIEVAL":
		n = string(types.StorageClassGlacier)
	case "GLACIER_INSTANT_RETRIEVAL":
		n = string(types.StorageClassGlacierIr)
	}
	for _, v := range types.StorageClassStandard.Values() {
		if string(v) == n {
			return v, nil
		}
	}
	return "", fmt.Errorf("unknown storage class %q", s)
}
