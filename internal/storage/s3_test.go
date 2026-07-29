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
