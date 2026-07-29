package storage

import (
	"context"
	"sync"

	"github.com/aws/smithy-go/middleware"
)

// APIStats はコストに影響する S3 API 操作の発行回数を集計する。
// SDK のミドルウェアとして各操作に一度ずつフックするため、マルチパート
// アップロードの各パートや ListObjectsV2 のページングも実際の回数で数える。
// (リトライは操作単位では二重計上しない。)
type APIStats struct {
	mu     sync.Mutex
	counts map[string]int64 // 操作名 -> 回数
}

// NewAPIStats は空の集計器を作る。
func NewAPIStats() *APIStats {
	return &APIStats{counts: make(map[string]int64)}
}

func (s *APIStats) record(op string) {
	s.mu.Lock()
	s.counts[op]++
	s.mu.Unlock()
}

// attach は SDK のミドルウェアスタックに操作カウンタを登録する。
// s3.Options.APIOptions に渡して使う。スタック ID がその操作の名前
// (例: "PutObject")なので、Initialize ステップで一度だけ計上する。
func (s *APIStats) attach(stack *middleware.Stack) error {
	op := stack.ID()
	return stack.Initialize.Add(
		middleware.InitializeMiddlewareFunc(
			"backupCSEAPICounter",
			func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
				s.record(op)
				return next.HandleInitialize(ctx, in)
			},
		),
		middleware.Before,
	)
}

// billingClass は S3 リクエストの課金クラス。
type billingClass int

const (
	classPutList billingClass = iota // PUT/COPY/POST/LIST 系(高い方の課金クラス)
	classGet                         // GET/HEAD 系(安い方の課金クラス)
	classRestore                     // RestoreObject(取り出しリクエスト)
	classFree                        // DELETE/CANCEL 系(無料)
)

// classifyOp は操作名を課金クラスに分類する。未知の操作は安全側(GET 相当)に寄せる。
func classifyOp(op string) billingClass {
	switch op {
	case "PutObject", "CopyObject", "UploadPartCopy",
		"CreateMultipartUpload", "UploadPart", "CompleteMultipartUpload",
		"ListObjectsV2", "ListObjects", "ListMultipartUploads", "ListParts",
		"ListObjectVersions", "ListBuckets":
		return classPutList
	case "RestoreObject":
		return classRestore
	case "DeleteObject", "DeleteObjects", "AbortMultipartUpload":
		return classFree
	default:
		// GetObject, HeadObject, HeadBucket など。
		return classGet
	}
}

// BillableSummary はコストに効く API 呼び出しの集計結果。
type BillableSummary struct {
	PutList int64            // PUT/COPY/POST/LIST リクエスト
	Get     int64            // GET/HEAD リクエスト
	Restore int64            // RestoreObject リクエスト
	Total   int64            // 課金対象の合計(無料の DELETE 等は含まない)
	ByOp    map[string]int64 // 操作名ごとの生カウント(無料操作も含む)
}

// Billable は現時点の集計スナップショットを課金クラス別に返す。
func (s *APIStats) Billable() BillableSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := BillableSummary{ByOp: make(map[string]int64, len(s.counts))}
	for op, n := range s.counts {
		out.ByOp[op] = n
		switch classifyOp(op) {
		case classPutList:
			out.PutList += n
			out.Total += n
		case classGet:
			out.Get += n
			out.Total += n
		case classRestore:
			out.Restore += n
			out.Total += n
		case classFree:
			// 無料。合計には含めない。
		}
	}
	return out
}
