package main

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen      string        `yaml:"listen"`
	WebDir      string        `yaml:"web_dir"`
	NoVNCDir    string        `yaml:"novnc_dir"`   // e.g. /usr/share/novnc
	SavesRoot   string        `yaml:"saves_root"`     // /srv/df/users
	ImageSDL    string        `yaml:"image_sdl"`      // df-image-sdl
	ImageText   string        `yaml:"image_text"`     // df-image-text
	Network     string        `yaml:"docker_network"` // df_internal
	IdleTimeout time.Duration `yaml:"idle_timeout"`   // 30m
	MaxSessions int           `yaml:"max_sessions"`   // 5
	CookieKey   string        `yaml:"cookie_key"`     // 32+ random bytes (hex)
	OIDCIssuer  string        `yaml:"oidc_issuer"`    // https://nextcloud.example.com
	OIDCClient  string        `yaml:"oidc_client_id"`
	OIDCSecret  string        `yaml:"oidc_client_secret"`
	OIDCRedirect string       `yaml:"oidc_redirect_uri"`
	RPOrigins   []string      `yaml:"rp_origins"`     // WebAuthn relying party origins
	RPID        string        `yaml:"rp_id"`          // WebAuthn relying party ID (hostname)
	RPName      string        `yaml:"rp_display_name"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = 30 * time.Minute
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = 5
	}
	if cfg.ImageSDL == "" {
		cfg.ImageSDL = "df-image-sdl"
	}
	if cfg.ImageText == "" {
		cfg.ImageText = "df-image-text"
	}
	if cfg.Network == "" {
		cfg.Network = "df_internal"
	}
	if cfg.SavesRoot == "" {
		cfg.SavesRoot = "/srv/df/users"
	}
	return &cfg, nil
}
