// Package crypt はバックアップデータのクライアントサイド暗号化を提供する。
//
// フォーマット:
//
//	magic(6) || noncePrefix(8) || chunk*
//
// 各チャンクは平文 ChunkSize バイトごとに AES-256-GCM で封印される。
// nonce は noncePrefix(8) || counter(3, big-endian) || lastFlag(1) の 12 バイトで、
// 最終チャンクのみ lastFlag=1 とすることでストリーム末尾の切り詰め改ざんを検知する。
package crypt

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ChunkSize は1チャンクあたりの平文サイズ。
const ChunkSize = 1 << 20

const (
	keySize         = 32
	noncePrefixSize = 8
	nonceSize       = 12
	tagSize         = 16
	counterMax      = 1<<24 - 1
)

var magic = []byte("BCSE1\x00")

var (
	ErrFormat    = errors.New("crypt: not a backup-cse encrypted stream")
	ErrCorrupt   = errors.New("crypt: data corrupted or wrong key")
	ErrTruncated = errors.New("crypt: encrypted stream is truncated")
)

// CiphertextSize は plaintextSize バイトの平文を Encrypt したときの出力バイト数を返す。
// 出力は header(magic+noncePrefix)+ 平文 + チャンク数ぶんの GCM タグ。
// 空入力でも最終チャンク(タグのみ)が1つ出るため、チャンク数は最低 1。
func CiphertextSize(plaintextSize int64) int64 {
	if plaintextSize < 0 {
		plaintextSize = 0
	}
	chunks := plaintextSize / ChunkSize
	if plaintextSize%ChunkSize != 0 {
		chunks++
	}
	if chunks == 0 {
		chunks = 1
	}
	return int64(len(magic)+noncePrefixSize) + plaintextSize + chunks*tagSize
}

// Key は AES-256-GCM の共通鍵。
type Key [keySize]byte

// GenerateKey は暗号学的乱数から新しい鍵を生成する。
func GenerateKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return Key{}, fmt.Errorf("generate key: %w", err)
	}
	return k, nil
}

// ParseKey は base64(std) 文字列から鍵を復元する。
func ParseKey(s string) (Key, error) {
	var k Key
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return Key{}, fmt.Errorf("parse key: %w", err)
	}
	if len(raw) != keySize {
		return Key{}, fmt.Errorf("parse key: got %d bytes, want %d", len(raw), keySize)
	}
	copy(k[:], raw)
	return k, nil
}

// String は鍵を base64(std) で表現する。
func (k Key) String() string {
	return base64.StdEncoding.EncodeToString(k[:])
}

// LoadKeyFile は鍵ファイル(base64 テキスト)を読み込む。
func LoadKeyFile(path string) (Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Key{}, fmt.Errorf("read key file: %w", err)
	}
	k, err := ParseKey(string(data))
	if err != nil {
		return Key{}, fmt.Errorf("key file %s: %w", path, err)
	}
	return k, nil
}

func newAEAD(key Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func makeNonce(prefix []byte, counter uint32, last bool) []byte {
	n := make([]byte, nonceSize)
	copy(n, prefix)
	n[8] = byte(counter >> 16)
	n[9] = byte(counter >> 8)
	n[10] = byte(counter)
	if last {
		n[11] = 1
	}
	return n
}

// Encrypt は src の平文を暗号化して dst に書き込む。
func Encrypt(dst io.Writer, src io.Reader, key Key) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	prefix := make([]byte, noncePrefixSize)
	if _, err := rand.Read(prefix); err != nil {
		return err
	}
	if _, err := dst.Write(magic); err != nil {
		return err
	}
	if _, err := dst.Write(prefix); err != nil {
		return err
	}

	br := bufio.NewReader(src)
	plain := make([]byte, ChunkSize)
	sealed := make([]byte, 0, ChunkSize+tagSize)
	for counter := uint32(0); ; counter++ {
		if counter > counterMax {
			return errors.New("crypt: input too large")
		}
		n, rerr := io.ReadFull(br, plain)
		var last bool
		switch {
		case rerr == nil:
			// チャンクを満たした。後続データの有無で最終チャンクか判定する。
			if _, perr := br.Peek(1); perr == io.EOF {
				last = true
			} else if perr != nil {
				return perr
			}
		case errors.Is(rerr, io.ErrUnexpectedEOF), errors.Is(rerr, io.EOF):
			last = true
		default:
			return rerr
		}
		out := aead.Seal(sealed[:0], makeNonce(prefix, counter, last), plain[:n], nil)
		if _, err := dst.Write(out); err != nil {
			return err
		}
		if last {
			return nil
		}
	}
}

// Decrypt は src の暗号化ストリームを復号して dst に書き込む。
func Decrypt(dst io.Writer, src io.Reader, key Key) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	br := bufio.NewReader(src)
	header := make([]byte, len(magic)+noncePrefixSize)
	if _, err := io.ReadFull(br, header); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrFormat
		}
		return err
	}
	if !bytes.Equal(header[:len(magic)], magic) {
		return ErrFormat
	}
	prefix := header[len(magic):]

	buf := make([]byte, ChunkSize+tagSize)
	plain := make([]byte, 0, ChunkSize)
	for counter := uint32(0); ; counter++ {
		if counter > counterMax {
			return ErrCorrupt
		}
		n, rerr := io.ReadFull(br, buf)
		var last bool
		switch {
		case rerr == nil:
			if _, perr := br.Peek(1); perr == io.EOF {
				last = true
			} else if perr != nil {
				return perr
			}
		case errors.Is(rerr, io.ErrUnexpectedEOF):
			last = true
		case errors.Is(rerr, io.EOF):
			// 直前のチャンクが最終フラグ無しで終わっている: 切り詰められている。
			return ErrTruncated
		default:
			return rerr
		}
		if n < tagSize {
			return ErrTruncated
		}
		pt, oerr := aead.Open(plain[:0], makeNonce(prefix, counter, last), buf[:n], nil)
		if oerr != nil {
			return ErrCorrupt
		}
		if _, err := dst.Write(pt); err != nil {
			return err
		}
		if last {
			return nil
		}
	}
}

// EncryptingReader は src を暗号化しながら読み出せる Reader を返す。
// S3 への streaming upload 用。
func EncryptingReader(src io.Reader, key Key) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(Encrypt(pw, src, key))
	}()
	return pr
}
