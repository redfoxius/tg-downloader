// Package config loads and saves the YAML config; env vars override the file.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	APIID       int     `yaml:"api_id"`
	APIHash     string  `yaml:"api_hash"`
	SessionPath string  `yaml:"session_path"`
	DataDir     string  `yaml:"data_dir"`
	Addr        string  `yaml:"addr"`
	RateLimit   float64 `yaml:"rate_limit"` // Telegram API requests per second
}

// DefaultPath is ~/.config/tgarchive/config.yaml.
func DefaultPath() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "tgarchive", "config.yaml")
}

// Load reads path (a missing file is fine), applies env overrides and defaults.
func Load(path string) (*Config, error) {
	c := &Config{}
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if v := os.Getenv("TG_API_ID"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("TG_API_ID: %w", err)
		}
		c.APIID = id
	}
	if v := os.Getenv("TG_API_HASH"); v != "" {
		c.APIHash = v
	}
	if c.SessionPath == "" {
		c.SessionPath = filepath.Join(filepath.Dir(path), "session.json")
	}
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8181"
	}
	if c.RateLimit <= 0 {
		c.RateLimit = 5
	}
	return c, nil
}

// Save writes the config with 0600 perms (it holds api_hash).
func (c *Config) Save(path string) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
