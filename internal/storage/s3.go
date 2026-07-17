// Package storage は S3 への暗号化アップロード・ダウンロードを提供する。
// アップロードされる全オブジェクトの内容は crypt パッケージで暗号化される。
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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
