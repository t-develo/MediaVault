package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config は config.yaml の内容を表す。
type Config struct {
	Listen    string         `yaml:"listen"`
	MediaRoot string         `yaml:"media_root"`
	CacheDir  string         `yaml:"cache_dir"`
	DBPath    string         `yaml:"db_path"`
	SessionTTLDays int       `yaml:"session_ttl_days"`
	TrustProxy bool          `yaml:"trust_proxy"`
	Auth      AuthConfig     `yaml:"auth"`
	IPBlock   IPBlockConfig  `yaml:"ipblock"`
	Extensions ExtConfig     `yaml:"extensions"`
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
	if len(c.Extensions.Image) == 0 {
		c.Extensions.Image = []string{"jpg", "jpeg", "png", "gif", "webp"}
	}
	if len(c.Extensions.Video) == 0 {
		// mp4/m4v/webm はブラウザがそのまま再生。avi/wmv/mkv/mov/flv 等は
		// 非対応コーデックが多いため、再生時に ffmpeg で都度トランスコードする。
		c.Extensions.Video = []string{"mp4", "m4v", "webm", "mov", "avi", "wmv", "mkv", "flv"}
	}
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
