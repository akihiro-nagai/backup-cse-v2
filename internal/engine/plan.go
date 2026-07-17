package engine

import "backup-cse/internal/conceal"

// backupItem はアップロード対象の1ファイル。
type backupItem struct {
	file      localFile
	concealed string // data/ 以下の秘匿化済みキー。dry-run の新規ファイルでは空。
	reason    string
}

// needsUpload はローカルファイル f をアップロードすべきか判定する。
// rec は conceal DB 上の既存レコード(ok=false なら未登録)、
// remote は S3 上に実在する data/ 以下のキー集合。
func needsUpload(rec conceal.FileRecord, ok bool, f localFile, remote map[string]struct{}) (need bool, reason string) {
	if !ok {
		return true, "new"
	}
	if rec.Size != f.size || rec.MTimeNano != f.mtimeNano {
		return true, "modified"
	}
	if _, exists := remote[rec.Concealed]; !exists {
		return true, "missing-on-remote"
	}
	return false, ""
}
