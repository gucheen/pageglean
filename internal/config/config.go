package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	Addr              string
	PublicURL         string
	PublicOrigin      string
	RPID              string
	DataDir           string
	DatabasePath      string
	SecureCookies     bool
	AllowPrivateFetch bool
}

func Load() (Config, error) {
	publicURL := envOr("LINKS_PUBLIC_URL", "http://localhost:8080")
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return Config{}, fmt.Errorf("parse LINKS_PUBLIC_URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Config{}, fmt.Errorf("LINKS_PUBLIC_URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return Config{}, fmt.Errorf("LINKS_PUBLIC_URL must include a hostname")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return Config{}, fmt.Errorf("LINKS_PUBLIC_URL must not include a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return Config{}, fmt.Errorf("LINKS_PUBLIC_URL must be an origin without credentials, query, or fragment")
	}

	rpID := strings.TrimSpace(os.Getenv("LINKS_RP_ID"))
	if rpID == "" {
		rpID = parsed.Hostname()
	}
	dataDir := envOr("LINKS_DATA_DIR", "./data")
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve LINKS_DATA_DIR: %w", err)
	}

	origin := parsed.Scheme + "://" + parsed.Host
	return Config{
		Addr:              envOr("LINKS_ADDR", ":8080"),
		PublicURL:         strings.TrimRight(origin, "/"),
		PublicOrigin:      origin,
		RPID:              rpID,
		DataDir:           absDataDir,
		DatabasePath:      filepath.Join(absDataDir, "links.db"),
		SecureCookies:     parsed.Scheme == "https",
		AllowPrivateFetch: strings.EqualFold(strings.TrimSpace(os.Getenv("LINKS_ALLOW_PRIVATE_FETCH")), "true"),
	}, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
