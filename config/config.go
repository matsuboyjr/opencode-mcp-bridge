// Modified for OpenCode V1/V2 configuration support.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddress  string
	OpenCodeURL    string
	Username       string
	Password       string
	APIVersion     string
	Directory      string
	RequestTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:  envOrDefault("BRIDGE_LISTEN_ADDRESS", ":8080"),
		OpenCodeURL:    strings.TrimRight(envOrDefault("OPENCODE_BASE_URL", "http://127.0.0.1:4096"), "/"),
		Username:       os.Getenv("OPENCODE_SERVER_USERNAME"),
		Password:       os.Getenv("OPENCODE_SERVER_PASSWORD"),
		APIVersion:     envOrDefault("OPENCODE_API_VERSION", "auto"),
		Directory:      os.Getenv("OPENCODE_DIRECTORY"),
		RequestTimeout: 60 * time.Minute,
	}
	if cfg.APIVersion != "auto" && cfg.APIVersion != "v1" && cfg.APIVersion != "v2" {
		return Config{}, fmt.Errorf("OPENCODE_API_VERSION must be auto, v1, or v2")
	}
	if cfg.Password != "" && cfg.Username == "" { cfg.Username = "opencode" }
	if cfg.Directory != "" && !serverAbsolutePath(cfg.Directory) {
		return Config{}, fmt.Errorf("OPENCODE_DIRECTORY must be an absolute path on the OpenCode server")
	}
	if value := os.Getenv("BRIDGE_REQUEST_TIMEOUT"); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("BRIDGE_REQUEST_TIMEOUT must be a positive duration")
		}
		cfg.RequestTimeout = d
	}
	if cfg.OpenCodeURL == "" {
		return Config{}, fmt.Errorf("OPENCODE_BASE_URL cannot be empty")
	}
	parsedURL, err := url.Parse(cfg.OpenCodeURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return Config{}, fmt.Errorf("OPENCODE_BASE_URL must be an absolute HTTP(S) URL")
	}
	return cfg, nil
}

func serverAbsolutePath(path string) bool {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\\`) {
		return true
	}
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
