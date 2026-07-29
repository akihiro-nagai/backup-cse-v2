package storage

import "testing"

func TestAPIStatsBillable(t *testing.T) {
	s := NewAPIStats()
	// PUT/LIST クラス
	s.record("PutObject")
	s.record("PutObject")
	s.record("CreateMultipartUpload")
	s.record("UploadPart")
	s.record("UploadPart")
	s.record("CompleteMultipartUpload")
	s.record("ListObjectsV2")
	// GET クラス
	s.record("GetObject")
	s.record("HeadObject")
	s.record("HeadObject")
	// RestoreObject
	s.record("RestoreObject")
	// 無料(合計に含めない)
	s.record("DeleteObjects")
	s.record("AbortMultipartUpload")

	got := s.Billable()
	if got.PutList != 7 {
		t.Errorf("PutList = %d, want 7", got.PutList)
	}
	if got.Get != 3 {
		t.Errorf("Get = %d, want 3", got.Get)
	}
	if got.Restore != 1 {
		t.Errorf("Restore = %d, want 1", got.Restore)
	}
	// 課金対象合計は 7 + 3 + 1 = 11(DELETE 系は含まない)。
	if got.Total != 11 {
		t.Errorf("Total = %d, want 11", got.Total)
	}
	// ByOp は無料操作も含めて全て記録する。
	if got.ByOp["DeleteObjects"] != 1 || got.ByOp["AbortMultipartUpload"] != 1 {
		t.Errorf("ByOp missing free ops: %v", got.ByOp)
	}
	if got.ByOp["UploadPart"] != 2 {
		t.Errorf("ByOp[UploadPart] = %d, want 2", got.ByOp["UploadPart"])
	}
}

func TestClassifyOp(t *testing.T) {
	cases := map[string]billingClass{
		"PutObject":               classPutList,
		"CopyObject":              classPutList,
		"CreateMultipartUpload":   classPutList,
		"UploadPart":              classPutList,
		"CompleteMultipartUpload": classPutList,
		"ListObjectsV2":           classPutList,
		"GetObject":               classGet,
		"HeadObject":              classGet,
		"RestoreObject":           classRestore,
		"DeleteObject":            classFree,
		"DeleteObjects":           classFree,
		"AbortMultipartUpload":    classFree,
		"SomethingUnknown":        classGet, // 未知は安全側(安いクラス)へ
	}
	for op, want := range cases {
		if got := classifyOp(op); got != want {
			t.Errorf("classifyOp(%q) = %d, want %d", op, got, want)
		}
	}
}
