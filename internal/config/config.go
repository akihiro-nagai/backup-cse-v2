// Package config は backup-cse の YAML 設定を読み込む。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"backup-cse/internal/crypt"
)

// KeyEnvVar が設定されている場合、鍵ファイルよりも優先される。
const KeyEnvVar = "BACKUP_CSE_KEY"

type Config struct {
	Destination Destination       `yaml:"destination"`
	Encryption  Encryption        `yaml:"encryption"`
	Sources     map[string]Source `yaml:"sources"`

	// dir は設定ファイルのあるディレクトリ。相対パスの解決に使う。
	dir string
}

type Destination struct {
	Bucket Bucket `yaml:"bucket"`
}

type Bucket struct {
	Bucket string `yaml:"bucket"`
	Prefix string `yaml:"prefix"`
	Region string `yaml:"region"`
}

type Encryption struct {
	// KeyFile は base64 テキストの 256bit 鍵ファイルへのパス。
	// 相対パスは設定ファイルのディレクトリ基準で解決される。
	KeyFile string `yaml:"key-file"`
}

type Source struct {
	Path         string   `yaml:"path"`
	StorageClass string   `yaml:"storage-class"`
	Excludes     []string `yaml:"excludes"`
}

// Load は YAML 設定ファイルを読み込み検証する。
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	var c Config
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.dir = filepath.Dir(abs)

	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if c.Destination.Bucket.Prefix != "" && !strings.HasSuffix(c.Destination.Bucket.Prefix, "/") {
		c.Destination.Bucket.Prefix += "/"
	}
	return &c, nil
}

func (c *Config) validate() error {
	b := c.Destination.Bucket
	if b.Bucket == "" {
		return errors.New("destination.bucket.bucket is required")
	}
	if b.Region == "" {
		return errors.New("destination.bucket.region is required")
	}
	if len(c.Sources) == 0 {
		return errors.New("at least one source is required")
	}
	for name, s := range c.Sources {
		if s.Path == "" {
			return fmt.Errorf("sources.%s.path is required", name)
		}
		if _, err := s.CompiledExcludes(); err != nil {
			return fmt.Errorf("sources.%s: %w", name, err)
		}
	}
	return nil
}

// Source は名前でソース設定を返す。
func (c *Config) Source(name string) (Source, error) {
	s, ok := c.Sources[name]
	if !ok {
		names := make([]string, 0, len(c.Sources))
		for n := range c.Sources {
			names = append(names, n)
		}
		sort.Strings(names)
		return Source{}, fmt.Errorf("unknown source %q (available: %s)", name, strings.Join(names, ", "))
	}
	return s, nil
}

// LoadKey は暗号鍵を解決する。環境変数 BACKUP_CSE_KEY (base64) が
// 設定されていればそれを使い、無ければ encryption.key-file を読む。
func (c *Config) LoadKey() (crypt.Key, error) {
	if v := os.Getenv(KeyEnvVar); v != "" {
		return crypt.ParseKey(v)
	}
	if c.Encryption.KeyFile == "" {
		return crypt.Key{}, fmt.Errorf("encryption key not configured: set encryption.key-file in config or %s env var (generate one with: backup-cse keygen <file>)", KeyEnvVar)
	}
	p := c.Encryption.KeyFile
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.dir, p)
	}
	return crypt.LoadKeyFile(p)
}

// CompiledExcludes は除外パターン(正規表現)をコンパイルして返す。
// パターンはスラッシュ区切りのフルパスに対してマッチングされる。
func (s Source) CompiledExcludes() ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(s.Excludes))
	for _, p := range s.Excludes {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}
