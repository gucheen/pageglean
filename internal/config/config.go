package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	WebhookURL        string
	WebhookSecret     string
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
	publicURL := envOr("PAGEGLEAN_PUBLIC_URL", "http://localhost:8080")
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return Config{}, fmt.Errorf("parse PAGEGLEAN_PUBLIC_URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must include a hostname")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must not include a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must be an origin without credentials, query, or fragment")
	}

	rpID := strings.TrimSpace(os.Getenv("PAGEGLEAN_RP_ID"))
	if rpID == "" {
		rpID = parsed.Hostname()
	}
	dataDir := envOr("PAGEGLEAN_DATA_DIR", "./data")
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve PAGEGLEAN_DATA_DIR: %w", err)
	}
	webhookURL := strings.TrimSpace(os.Getenv("PAGEGLEAN_WEBHOOK_URL"))
	webhookSecret := os.Getenv("PAGEGLEAN_WEBHOOK_SECRET")
	if err := ValidateWebhook(webhookURL, webhookSecret); err != nil {
		return Config{}, err
	}
	origin := parsed.Scheme + "://" + parsed.Host
	return Config{
		WebhookURL: webhookURL, WebhookSecret: webhookSecret, Addr: envOr("PAGEGLEAN_ADDR", ":8080"),
		PublicURL:         strings.TrimRight(origin, "/"),
		PublicOrigin:      origin,
		RPID:              rpID,
		DataDir:           absDataDir,
		DatabasePath:      filepath.Join(absDataDir, "pageglean.db"),
		SecureCookies:     parsed.Scheme == "https",
		AllowPrivateFetch: strings.EqualFold(strings.TrimSpace(os.Getenv("PAGEGLEAN_ALLOW_PRIVATE_FETCH")), "true"),
	}, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func ValidateWebhook(endpoint, secret string) error {
	if endpoint == "" && secret == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("PAGEGLEAN_WEBHOOK_URL must be an HTTP(S) URL without credentials or a fragment")
	}
	if len(secret) < 32 {
		return fmt.Errorf("PAGEGLEAN_WEBHOOK_SECRET must contain at least 32 bytes")
	}
	return nil
}
