package config

import (
	"os"
	"path/filepath"
	"testing"

	"backup-cse/internal/crypt"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validYAML = `
destination:
  bucket:
    bucket: my-backup-bucket
    prefix: my-macbook-backups
    region: us-west-2

encryption:
  key-file: backup.key

sources:
  photo:
    path: /Users/aki/photo
    storage-class: standard
  programs:
    path: /Users/aki/programs
    storage-class: glacier-deep-archive
    excludes:
      - .*/node_modules/.*
`

func TestLoad(t *testing.T) {
	c, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Destination.Bucket.Bucket != "my-backup-bucket" {
		t.Errorf("bucket = %q", c.Destination.Bucket.Bucket)
	}
	if c.Destination.Bucket.Region != "us-west-2" {
		t.Errorf("region = %q", c.Destination.Bucket.Region)
	}
	// prefix には末尾スラッシュが補われる。
	if c.Destination.Bucket.Prefix != "my-macbook-backups/" {
		t.Errorf("prefix = %q", c.Destination.Bucket.Prefix)
	}

	s, err := c.Source("programs")
	if err != nil {
		t.Fatal(err)
	}
	if s.Path != "/Users/aki/programs" || s.StorageClass != "glacier-deep-archive" {
		t.Errorf("source = %+v", s)
	}
	res, err := s.CompiledExcludes()
	if err != nil || len(res) != 1 {
		t.Fatalf("excludes: %v %v", res, err)
	}

	if _, err := c.Source("nope"); err == nil {
		t.Error("unknown source accepted")
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"unknown field": `
destination:
  bucket:
    bucket: b
    region: r
sources:
  a:
    path: /x
    storage_class: standard
`,
		"missing region": `
destination:
  bucket:
    bucket: b
sources:
  a:
    path: /x
`,
		"missing bucket": `
destination:
  bucket:
    region: r
sources:
  a:
    path: /x
`,
		"no sources": `
destination:
  bucket:
    bucket: b
    region: r
`,
		"source without path": `
destination:
  bucket:
    bucket: b
    region: r
sources:
  a:
    storage-class: standard
`,
		"broken exclude regex": `
destination:
  bucket:
    bucket: b
    region: r
sources:
  a:
    path: /x
    excludes:
      - "([unclosed"
`,
	}
	for name, yaml := range cases {
		if _, err := Load(writeConfig(t, yaml)); err == nil {
			t.Errorf("%s: invalid config accepted", name)
		}
	}
}

func TestLoadKeyFromEnv(t *testing.T) {
	k, err := crypt.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(KeyEnvVar, k.String())
	c, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	if got != k {
		t.Fatal("env key mismatch")
	}
}

func TestLoadKeyFromFileRelativeToConfig(t *testing.T) {
	t.Setenv(KeyEnvVar, "")
	path := writeConfig(t, validYAML)
	k, err := crypt.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(filepath.Dir(path), "backup.key")
	if err := os.WriteFile(keyPath, []byte(k.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.LoadKey()
	if err != nil {
		t.Fatal(err)
	}
	if got != k {
		t.Fatal("file key mismatch")
	}
}

func TestLoadKeyMissing(t *testing.T) {
	t.Setenv(KeyEnvVar, "")
	yaml := `
destination:
  bucket:
    bucket: b
    region: r
sources:
  a:
    path: /x
`
	c, err := Load(writeConfig(t, yaml))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.LoadKey(); err == nil {
		t.Fatal("missing key config accepted")
	}
}
