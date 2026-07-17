// Package conceal はオリジナルのファイル名・ディレクトリ名と、
// S3 上の秘匿化(UUID v4)されたパスの対応を bbolt に保存する。
package conceal

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var (
	bucketFiles = []byte("files")
	bucketDirs  = []byte("dirs")
)

// FileRecord は1ファイルのバックアップ状態。
// DB のキーはソースルートからの相対パス(スラッシュ区切り)。
type FileRecord struct {
	// Concealed は <source>/data/ 以下の秘匿化済み相対キー。
	// 各パス成分が UUID v4 になっている。
	Concealed string `json:"concealed"`
	Size      int64  `json:"size"`
	MTimeNano int64  `json:"mtime_unix_nano"`
}

// DB は秘匿化マッピングデータベース。
type DB struct {
	db *bolt.DB
}

// Open は path の bbolt DB を開く(無ければ作成する)。
func Open(path string) (*DB, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open conceal db: %w", err)
	}
	// この DB はテンポラリコピー上で更新し最後に S3 へアップロードするため、
	// コミット毎の fsync は不要。
	db.NoSync = true
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketFiles, bucketDirs} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("init conceal db: %w", err)
	}
	return &DB{db: db}, nil
}

// Close は DB を閉じる。以降ファイル内容は読み出し可能な最終状態になる。
func (d *DB) Close() error {
	if err := d.db.Sync(); err != nil {
		d.db.Close()
		return err
	}
	return d.db.Close()
}

// File は rel のレコードを返す。存在しない場合は ok=false。
func (d *DB) File(rel string) (rec FileRecord, ok bool, err error) {
	err = d.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketFiles).Get([]byte(rel))
		if v == nil {
			return nil
		}
		if uerr := json.Unmarshal(v, &rec); uerr != nil {
			return fmt.Errorf("conceal db: corrupt record for %q: %w", rel, uerr)
		}
		ok = true
		return nil
	})
	return rec, ok, err
}

// PutFile は rel のレコードを保存する。
func (d *DB) PutFile(rel string, rec FileRecord) error {
	v, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return d.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketFiles).Put([]byte(rel), v)
	})
}

// ConcealFilePaths は各相対パスに対する秘匿化パスをまとめて採番する。
// ディレクトリ成分の UUID は既存の割り当てを再利用し、ファイル成分には
// 新しい UUID v4 を割り当てる。
func (d *DB) ConcealFilePaths(rels []string) (map[string]string, error) {
	out := make(map[string]string, len(rels))
	err := d.db.Update(func(tx *bolt.Tx) error {
		dirs := tx.Bucket(bucketDirs)
		for _, rel := range rels {
			parts := strings.Split(rel, "/")
			comps := make([]string, 0, len(parts))
			for i := 0; i < len(parts)-1; i++ {
				key := []byte(strings.Join(parts[:i+1], "/"))
				u := dirs.Get(key)
				if u == nil {
					id := []byte(uuid.NewString())
					if err := dirs.Put(key, id); err != nil {
						return err
					}
					u = id
				}
				comps = append(comps, string(u))
			}
			comps = append(comps, uuid.NewString())
			out[rel] = strings.Join(comps, "/")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureDirs はディレクトリ(空ディレクトリを含む)を登録する。
// リストア時のディレクトリ構造復元に使う。
func (d *DB) EnsureDirs(rels []string) error {
	return d.db.Update(func(tx *bolt.Tx) error {
		dirs := tx.Bucket(bucketDirs)
		for _, rel := range rels {
			if rel == "" || rel == "." {
				continue
			}
			parts := strings.Split(rel, "/")
			for i := range parts {
				key := []byte(strings.Join(parts[:i+1], "/"))
				if dirs.Get(key) == nil {
					if err := dirs.Put(key, []byte(uuid.NewString())); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// Files は全ファイルレコードを列挙する。
func (d *DB) Files(fn func(rel string, rec FileRecord) error) error {
	return d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketFiles).ForEach(func(k, v []byte) error {
			var rec FileRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return fmt.Errorf("conceal db: corrupt record for %q: %w", k, err)
			}
			return fn(string(k), rec)
		})
	})
}

// Dirs は登録済みディレクトリの相対パスを列挙する。
func (d *DB) Dirs(fn func(rel string) error) error {
	return d.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketDirs).ForEach(func(k, _ []byte) error {
			return fn(string(k))
		})
	})
}
