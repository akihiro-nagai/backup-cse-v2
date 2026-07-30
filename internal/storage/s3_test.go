package storage

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestParseStorageClass(t *testing.T) {
	cases := []struct {
		in   string
		want types.StorageClass
	}{
		{"", types.StorageClassStandard},
		{"standard", types.StorageClassStandard},
		{"STANDARD", types.StorageClassStandard},
		{"standard-ia", types.StorageClassStandardIa},
		{"intelligent-tiering", types.StorageClassIntelligentTiering},
		{"glacier", types.StorageClassGlacier},
		{"glacier-ir", types.StorageClassGlacierIr},
		{"glacier-instant-retrieval", types.StorageClassGlacierIr},
		{"glacier-flexible-retrieval", types.StorageClassGlacier},
		{"deep-archive", types.StorageClassDeepArchive},
		{"glacier-deep-archive", types.StorageClassDeepArchive},
		{"DEEP_ARCHIVE", types.StorageClassDeepArchive},
	}
	for _, c := range cases {
		got, err := ParseStorageClass(c.in)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"gracier-deep-archive", "cheapest", "s3-standard"} {
		if _, err := ParseStorageClass(bad); err == nil {
			t.Errorf("%q: invalid storage class accepted", bad)
		}
	}
}

func TestUploadPartSize(t *testing.T) {
	const (
		mib = 1024 * 1024
		gib = 1024 * mib
		tib = 1024 * gib
	)
	// 小さいファイルは最小パートサイズ(5 MiB)のまま。
	for _, small := range []int64{0, 1, mib, 40 * gib} {
		got, err := uploadPartSize(small)
		if err != nil {
			t.Fatalf("uploadPartSize(%d): %v", small, err)
		}
		if got != s3MinPartSize {
			t.Errorf("uploadPartSize(%d) = %d, want %d (min)", small, got, s3MinPartSize)
		}
	}

	// 5 TiB を超えるとエラー。
	if _, err := uploadPartSize(s3MaxObjectSize + 1); err == nil {
		t.Error("expected error for object over 5 TiB")
	}

	// 各サイズでパート数が S3 の上限(10,000)以内かつパートサイズが 5 GiB 以内。
	for _, size := range []int64{60 * gib, 160 * gib, 500 * gib, tib, 5 * tib} {
		part, err := uploadPartSize(size)
		if err != nil {
			t.Fatalf("uploadPartSize(%d): %v", size, err)
		}
		if part < s3MinPartSize || part > s3MaxPartSize {
			t.Errorf("size %d: part %d out of range [%d,%d]", size, part, s3MinPartSize, s3MaxPartSize)
		}
		parts := (size + part - 1) / part
		if parts > s3MaxUploadParts {
			t.Errorf("size %d: %d parts of %d bytes exceeds max %d", size, parts, part, s3MaxUploadParts)
		}
	}

	// 160 GB は既定の 5 MiB では上限超過(回帰の再現)。今の算出では収まる。
	part, _ := uploadPartSize(160 * gib)
	if (160*gib+s3MinPartSize-1)/s3MinPartSize <= s3MaxUploadParts {
		t.Fatal("test premise wrong: 160GiB should exceed max parts at 5MiB")
	}
	if parts := (160*gib + part - 1) / part; parts > s3MaxUploadParts {
		t.Errorf("160GiB still exceeds max parts: %d", parts)
	}
}

func TestParseTier(t *testing.T) {
	cases := []struct {
		in   string
		want types.Tier
	}{
		{"", types.TierStandard},
		{"standard", types.TierStandard},
		{"STANDARD", types.TierStandard},
		{"bulk", types.TierBulk},
		{"Bulk", types.TierBulk},
		{"expedited", types.TierExpedited},
	}
	for _, c := range cases {
		got, err := ParseTier(c.in)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %s, want %s", c.in, got, c.want)
		}
	}
	if _, err := ParseTier("fast"); err == nil {
		t.Error("invalid tier accepted")
	}
}

func TestNeedsRestore(t *testing.T) {
	cases := []struct {
		class types.StorageClass
		arch  types.ArchiveStatus
		want  bool
	}{
		{types.StorageClassStandard, "", false},
		{types.StorageClassStandardIa, "", false},
		{types.StorageClassGlacierIr, "", false},
		{types.StorageClassGlacier, "", true},
		{types.StorageClassDeepArchive, "", true},
		{types.StorageClassIntelligentTiering, "", false},
		{types.StorageClassIntelligentTiering, types.ArchiveStatusArchiveAccess, true},
		{types.StorageClassIntelligentTiering, types.ArchiveStatusDeepArchiveAccess, true},
	}
	for _, c := range cases {
		if got := NeedsRestore(c.class, c.arch); got != c.want {
			t.Errorf("NeedsRestore(%s, %s) = %v, want %v", c.class, c.arch, got, c.want)
		}
	}
}

func TestClassifyRestore(t *testing.T) {
	now, err := time.Parse(time.RFC3339, "2026-07-29T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		class  types.StorageClass
		header string
		want   RestoreState
	}{
		{"standard needs no restore", types.StorageClassStandard, "", RestoreNotNeeded},
		{"archived, never requested", types.StorageClassDeepArchive, "", RestoreNotRequested},
		{"archived, in progress", types.StorageClassGlacier, `ongoing-request="true"`, RestoreInProgress},
		{
			"archived, available",
			types.StorageClassDeepArchive,
			`ongoing-request="false", expiry-date="Thu, 30 Jul 2026 00:00:00 GMT"`,
			RestoreAvailable,
		},
		{
			"archived, expired restore window",
			types.StorageClassDeepArchive,
			`ongoing-request="false", expiry-date="Mon, 20 Jul 2026 00:00:00 GMT"`,
			RestoreNotRequested,
		},
		{"archived, unparsable header", types.StorageClassGlacier, `garbage`, RestoreNotRequested},
	}
	for _, c := range cases {
		if got := classifyRestore(c.class, "", c.header, now); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}
