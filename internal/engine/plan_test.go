package engine

import (
	"testing"

	"backup-cse/internal/conceal"
)

func TestNeedsUpload(t *testing.T) {
	f := localFile{rel: "a.txt", size: 100, mtimeNano: 5000}
	rec := conceal.FileRecord{Concealed: "u1", Size: 100, MTimeNano: 5000}
	remote := map[string]struct{}{"u1": {}}

	cases := []struct {
		name   string
		rec    conceal.FileRecord
		ok     bool
		remote map[string]struct{}
		need   bool
		reason string
	}{
		{"unchanged", rec, true, remote, false, ""},
		{"new file", conceal.FileRecord{}, false, remote, true, "new"},
		{"size changed", conceal.FileRecord{Concealed: "u1", Size: 99, MTimeNano: 5000}, true, remote, true, "modified"},
		{"mtime changed", conceal.FileRecord{Concealed: "u1", Size: 100, MTimeNano: 1}, true, remote, true, "modified"},
		{"missing on remote", rec, true, map[string]struct{}{}, true, "missing-on-remote"},
	}
	for _, c := range cases {
		need, reason := needsUpload(c.rec, c.ok, f, c.remote)
		if need != c.need || reason != c.reason {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", c.name, need, reason, c.need, c.reason)
		}
	}
}
