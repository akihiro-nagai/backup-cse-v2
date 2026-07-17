package storage

import (
	"testing"

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
