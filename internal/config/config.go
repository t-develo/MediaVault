package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// 組み込みの標準拡張子。config.yaml の extensions はこれらに「追加する」形で働く。
// （古い config.yaml でも、コードで対応形式を増やせば自動的に認識されるようにするため）
var (
	defaultImageExts = []string{"jpg", "jpeg", "png", "gif", "webp"}
	// mp4/m4v/webm/mov はブラウザがそのまま再生。avi/wmv/mkv/flv 等は
	// 非対応コーデックが多いため、再生時に ffmpeg で都度トランスコードする。
	defaultVideoExts = []string{"mp4", "m4v", "webm", "mov", "avi", "wmv", "mkv", "flv"}
)

// Config は config.yaml の内容を表す。
type Config struct {
	Listen         string        `yaml:"listen"`
	MediaRoot      string        `yaml:"media_root"`
	CacheDir       string        `yaml:"cache_dir"`
	DBPath         string        `yaml:"db_path"`
	SessionTTLDays int           `yaml:"session_ttl_days"`
	TrustProxy     bool          `yaml:"trust_proxy"`
	Auth           AuthConfig    `yaml:"auth"`
	IPBlock        IPBlockConfig `yaml:"ipblock"`
	Extensions     ExtConfig     `yaml:"extensions"`
	// HistoryExclude は閲覧履歴に残さないフォルダ。指定パスおよびその配下を除外する。
	HistoryExclude []string `yaml:"history_exclude"`
}

type AuthConfig struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
}

type IPBlockConfig struct {
	MaxFailures  int `yaml:"max_failures"`
	BlockMinutes int `yaml:"block_minutes"`
}

type ExtConfig struct {
	Image []string `yaml:"image"`
	Video []string `yaml:"video"`
}

// Load は指定パスの設定を読み込み、デフォルト補完と検証を行う。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("設定ファイルを読み込めません: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("設定ファイルの解析に失敗: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "0.0.0.0:8080"
	}
	if c.CacheDir == "" {
		c.CacheDir = "./cache"
	}
	if c.DBPath == "" {
		c.DBPath = "./mediavault.db"
	}
	if c.SessionTTLDays <= 0 {
		c.SessionTTLDays = 30
	}
	if c.IPBlock.MaxFailures <= 0 {
		c.IPBlock.MaxFailures = 5
	}
	if c.IPBlock.BlockMinutes <= 0 {
		c.IPBlock.BlockMinutes = 15
	}
	// 標準拡張子は常に含める（ユーザー追加分は維持、重複は排除・順序は標準が先）。
	// これにより既存の config.yaml を編集しなくても、対応形式の追加が反映される。
	c.Extensions.Image = unionExts(defaultImageExts, c.Extensions.Image)
	c.Extensions.Video = unionExts(defaultVideoExts, c.Extensions.Video)
}

// unionExts は base と extra を結合する。小文字・先頭ドット無しで正規化して
// 重複を排除し、base を先、extra の追加分を後ろに並べる。
func unionExts(base, extra []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(base)+len(extra))
	add := func(exts []string) {
		for _, e := range exts {
			norm := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
			if norm == "" || seen[norm] {
				continue
			}
			seen[norm] = true
			out = append(out, norm)
		}
	}
	add(base)
	add(extra)
	return out
}

// IsHistoryExcluded は relPath が history_exclude のいずれか、またはその配下なら true を返す。
func (c *Config) IsHistoryExcluded(relPath string) bool {
	target := path.Clean("/" + strings.TrimPrefix(relPath, "/"))
	for _, ex := range c.HistoryExclude {
		e := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(ex), "/"))
		if e == "/" || e == "" {
			continue
		}
		if target == e || strings.HasPrefix(target, e+"/") {
			return true
		}
	}
	return false
}

func (c *Config) validate() error {
	if c.MediaRoot == "" {
		return fmt.Errorf("media_root が設定されていません")
	}
	abs, err := filepath.Abs(c.MediaRoot)
	if err != nil {
		return fmt.Errorf("media_root の解決に失敗: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("media_root にアクセスできません (%s): %w", abs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("media_root はディレクトリではありません: %s", abs)
	}
	c.MediaRoot = abs
	return nil
}
