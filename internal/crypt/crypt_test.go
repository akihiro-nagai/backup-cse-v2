package crypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"testing"
)

func testKey(t *testing.T) Key {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func encryptBytes(t *testing.T, key Key, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Encrypt(&buf, bytes.NewReader(plain), key); err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	return buf.Bytes()
}

func decryptBytes(key Key, enc []byte) ([]byte, error) {
	var buf bytes.Buffer
	err := Decrypt(&buf, bytes.NewReader(enc), key)
	return buf.Bytes(), err
}

func TestRoundTrip(t *testing.T) {
	key := testKey(t)
	sizes := []int{0, 1, 15, 16, 1000, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2*ChunkSize + ChunkSize/2}
	for _, size := range sizes {
		plain := make([]byte, size)
		if _, err := rand.Read(plain); err != nil {
			t.Fatal(err)
		}
		enc := encryptBytes(t, key, plain)
		got, err := decryptBytes(key, enc)
		if err != nil {
			t.Fatalf("size %d: Decrypt: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: roundtrip mismatch", size)
		}
	}
}

func TestEncryptedOutputDiffersFromPlaintext(t *testing.T) {
	key := testKey(t)
	plain := []byte("hello, backup-cse")
	enc := encryptBytes(t, key, plain)
	if bytes.Contains(enc, plain) {
		t.Fatal("ciphertext contains plaintext")
	}
	// 同じ平文でも nonce prefix により毎回異なる暗号文になる。
	if bytes.Equal(enc, encryptBytes(t, key, plain)) {
		t.Fatal("two encryptions produced identical ciphertext")
	}
}

func TestWrongKeyFails(t *testing.T) {
	enc := encryptBytes(t, testKey(t), []byte("secret data"))
	if _, err := decryptBytes(testKey(t), enc); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestTamperDetection(t *testing.T) {
	key := testKey(t)
	enc := encryptBytes(t, key, bytes.Repeat([]byte("x"), 1000))
	enc[len(enc)-1] ^= 0x01
	if _, err := decryptBytes(key, enc); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("want ErrCorrupt, got %v", err)
	}
}

func TestTruncationDetection(t *testing.T) {
	key := testKey(t)
	plain := make([]byte, 2*ChunkSize+100)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	enc := encryptBytes(t, key, plain)

	headerLen := len(magic) + noncePrefixSize
	cuts := map[string]int{
		"mid-chunk":            len(enc) - 50,
		"chunk-boundary":       headerLen + (ChunkSize + tagSize),
		"after-header":         headerLen,
		"exactly-two-chunks":   headerLen + 2*(ChunkSize+tagSize),
		"partial-final-header": headerLen - 3,
	}
	for name, cut := range cuts {
		if _, err := decryptBytes(key, enc[:cut]); err == nil {
			t.Errorf("%s: truncated stream decrypted without error", name)
		}
	}
}

func TestNotAnEncryptedStream(t *testing.T) {
	key := testKey(t)
	if _, err := decryptBytes(key, []byte("this is not encrypted at all")); !errors.Is(err, ErrFormat) {
		t.Fatalf("want ErrFormat, got %v", err)
	}
	if _, err := decryptBytes(key, nil); !errors.Is(err, ErrFormat) {
		t.Fatalf("empty input: want ErrFormat, got %v", err)
	}
}

func TestEncryptingReader(t *testing.T) {
	key := testKey(t)
	plain := make([]byte, ChunkSize+123)
	if _, err := rand.Read(plain); err != nil {
		t.Fatal(err)
	}
	r := EncryptingReader(bytes.NewReader(plain), key)
	defer r.Close()
	enc, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptBytes(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("EncryptingReader roundtrip mismatch")
	}
}

func TestCiphertextSize(t *testing.T) {
	key := testKey(t)
	sizes := []int{0, 1, 15, 16, 1000, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3 * ChunkSize, 5*ChunkSize + 7}
	for _, size := range sizes {
		plain := make([]byte, size)
		enc := encryptBytes(t, key, plain)
		if got := CiphertextSize(int64(size)); got != int64(len(enc)) {
			t.Errorf("CiphertextSize(%d) = %d, actual encrypted len = %d", size, got, len(enc))
		}
	}
}

func TestKeyParseRoundTrip(t *testing.T) {
	k := testKey(t)
	got, err := ParseKey(k.String())
	if err != nil {
		t.Fatal(err)
	}
	if got != k {
		t.Fatal("key parse roundtrip mismatch")
	}
	if _, err := ParseKey("dG9vc2hvcnQ="); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := ParseKey("!!!not base64!!!"); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}
